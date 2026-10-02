package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
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

// cursorKey 是游标里编码的排序键 (created_at, id)：keyset 分页，不是 offset。
type cursorKey struct {
	CreatedAt time.Time
	ID        int64
}

// List 按 created_at、id 倒序分页。多取一条判断有没有下一页；最后一页的
// NextCursor 是空字符串。
func (r *Repo) List(ctx context.Context, in ListInput) (*ListResult, error) {
	q := buildListQuery(in)

	var ck *cursorKey
	if q.Cursor != "" {
		decoded, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, fmt.Errorf("非法 cursor：%w", err)
		}
		ck = &decoded
	}

	var out ListResult
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		query := `SELECT ` + customerColumns + `
			FROM customers
			WHERE created_at >= $1 AND created_at <= $2`
		args := []any{q.From, q.To}
		if in.StatusFilter != "" {
			args = append(args, in.StatusFilter)
			query += fmt.Sprintf(" AND status = $%d", len(args))
		}
		if ck != nil {
			args = append(args, ck.CreatedAt, ck.ID)
			query += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
		}
		args = append(args, q.Limit+1)
		query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

		customers, err := queryCustomers(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		if len(customers) > q.Limit {
			last := customers[q.Limit-1]
			lastRawID, err := strconv.ParseInt(last.ID, 10, 64)
			if err != nil {
				return err
			}
			out.NextCursor = encodeCursor(cursorKey{CreatedAt: last.CreatedAt, ID: lastRawID})
			customers = customers[:q.Limit]
		}
		out.Customers = customers
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func queryCustomers(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]*Customer, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查 customers: %w", err)
	}
	defer rows.Close()

	var customers []*Customer
	for rows.Next() {
		c, err := scanCustomerRow(rows)
		if err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}
