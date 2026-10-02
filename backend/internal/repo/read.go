package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// BatchGet 按 id 批量取客户，缺失的 id 进 missing、不报错。这是别的组件
// 防 N+1 的读法（一个列表页里的 N 个客户一次取回），只走 gRPC。热表 /
// 归档表的路由交给 besdk.BatchGetRouted：本组件不归档，归档 schema 里没有
// customers 表，SDK 查不到表时只用热表的结果。
func (r *Repo) BatchGet(ctx context.Context, ids []string) (found []*Customer, missing []string, err error) {
	var rows []*Customer
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		got, err := besdk.BatchGetRouted(ctx, tx, r.schema, "customers", ids, scanCustomer)
		if err != nil {
			return err
		}
		rows = got
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	foundIDs := make(map[string]bool, len(rows))
	for _, c := range rows {
		foundIDs[c.ID] = true
	}
	var missingIDs []string
	for _, id := range ids {
		if !foundIDs[id] {
			missingIDs = append(missingIDs, id)
		}
	}
	return rows, missingIDs, nil
}

// ListInput 对应 ListRequest。没有 offset：列表只能按游标翻页。
type ListInput struct {
	Q             string // 关键字：匹配 code 或 name，见 List
	Cursor        string
	PageSize      int
	StatusFilter  string
	CreatedAfter  time.Time
	CreatedBefore time.Time
}

type ListResult struct {
	Customers  []*Customer
	NextCursor string
}

// buildListQuery 把 ListInput 交给 besdk.ListWindow：没给时间范围时补上
// 默认窗口（最近 90 天），页大小取默认值或截到上限。算法是 SDK 的，这里
// 只保证接上了。
func buildListQuery(in ListInput) besdk.Query {
	return besdk.ListWindow(besdk.Query{
		From:   in.CreatedAfter,
		To:     in.CreatedBefore,
		Cursor: in.Cursor,
		Limit:  in.PageSize,
	})
}

// cursorKey 是游标里编码的排序键 (match_rank, created_at, id)：keyset 分页，
// 不是 offset。不带 q 时 match_rank 恒为 0。
type cursorKey struct {
	Rank      int
	CreatedAt time.Time
	ID        int64
}

// List 分页列出客户：先按 match_rank（带 q 时前缀匹配为 0、只是包含为 1；
// 不带 q 时恒为 0），再按 created_at、id 倒序。多取一条判断有没有下一页；
// 最后一页的 NextCursor 是空字符串。
//
// q 去掉首尾空白后为空就等于不过滤。匹配用 strpos / starts_with 而不是
// LIKE：用户输入里的 % 与 _ 按字面匹配，不必转义。lower() 让大小写不敏感。
// 默认时间窗口照常生效（与不带 q 的列表一致），要搜更早建档的客户就显式
// 给 CreatedAfter。
func (r *Repo) List(ctx context.Context, in ListInput) (*ListResult, error) {
	q := buildListQuery(in)

	var ck *cursorKey
	if q.Cursor != "" {
		decoded, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, fmt.Errorf("%w：%v", ErrInvalidCursor, err)
		}
		ck = &decoded
	}

	var out ListResult
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		query, args := listSQL(in, q, ck)
		rows, err := queryRanked(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		if len(rows) > q.Limit {
			last := rows[q.Limit-1]
			lastRawID, err := strconv.ParseInt(last.c.ID, 10, 64)
			if err != nil {
				return err
			}
			out.NextCursor = encodeCursor(cursorKey{Rank: last.rank, CreatedAt: last.c.CreatedAt, ID: lastRawID})
			rows = rows[:q.Limit]
		}
		out.Customers = make([]*Customer, 0, len(rows))
		for _, row := range rows {
			out.Customers = append(out.Customers, row.c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// listSQL 拼 List 的查询。内层算 match_rank 并做全部过滤，外层按游标切页。
func listSQL(in ListInput, q besdk.Query, ck *cursorKey) (string, []any) {
	args := []any{q.From, q.To}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	rank := "0"
	where := "created_at >= $1 AND created_at <= $2"
	if in.StatusFilter != "" {
		where += " AND status = " + arg(in.StatusFilter)
	}
	if kw := strings.TrimSpace(in.Q); kw != "" {
		p := "lower(" + arg(kw) + ")"
		where += fmt.Sprintf(" AND (strpos(lower(code), %s) > 0 OR strpos(lower(name), %s) > 0)", p, p)
		rank = fmt.Sprintf("CASE WHEN starts_with(lower(code), %s) OR starts_with(lower(name), %s) THEN 0 ELSE 1 END", p, p)
	}

	query := `SELECT ` + customerColumns + `, match_rank FROM (
			SELECT ` + customerColumns + `, ` + rank + ` AS match_rank
			FROM customers
			WHERE ` + where + `
		) t`
	if ck != nil {
		r, c, i := arg(ck.Rank), arg(ck.CreatedAt), arg(ck.ID)
		query += fmt.Sprintf(" WHERE (match_rank > %s OR (match_rank = %s AND (created_at, id) < (%s, %s)))", r, r, c, i)
	}
	query += " ORDER BY match_rank, created_at DESC, id DESC LIMIT " + arg(q.Limit+1)
	return query, args
}

type rankedCustomer struct {
	c    *Customer
	rank int
}

func queryRanked(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]rankedCustomer, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查 customers: %w", err)
	}
	defer rows.Close()

	var out []rankedCustomer
	for rows.Next() {
		var rawID int64
		var c Customer
		var rank int
		if err := rows.Scan(&rawID, &c.Code, &c.Name, &c.TaxNo, &c.CreditLimit,
			&c.CreatedAt, &c.UpdatedAt, &c.Version, &c.Status, &rank); err != nil {
			return nil, err
		}
		c.ID = strconv.FormatInt(rawID, 10)
		out = append(out, rankedCustomer{c: &c, rank: rank})
	}
	return out, rows.Err()
}
