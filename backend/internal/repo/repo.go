// Package repo 是 mdm-customer 的数据访问层：customers / contacts /
// billing_infos 三张表，加上同事务写入的 Outbox 与幂等键表。
//
// 文件按读写拆开：write.go 是四个写命令（每个都在一个事务里做"查幂等键 →
// 写业务行 → 记幂等键 → 写 Outbox"），read.go 是按 id 批量取与列表分页，
// cursor.go 是列表游标的编解码。本文件只放三边共用的类型、哨兵错误与扫描函数。
//
// 每次访问数据库都经 besdk.WithTx：它在事务里 SET LOCAL ROLE 与
// SET LOCAL search_path，连接还回池里时不留下本组件的设置。
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Repo 持有共享连接池与本组件的 role / schema。
type Repo struct {
	db     *sql.DB
	role   string
	schema string
}

func New(db *sql.DB, role, schema string) *Repo {
	return &Repo{db: db, role: role, schema: schema}
}

// Customer 是 customers 表的一行。CreditLimit 是十进制字符串：浮点数跨语言
// 序列化会丢精度。
type Customer struct {
	ID          string
	Code        string
	Name        string
	TaxNo       string
	CreditLimit string
	Status      string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Contact 是 contacts 表的一行。
type Contact struct {
	ID      string
	Name    string
	Phone   string
	Email   string
	Primary bool
}

// ErrVersionConflict 是乐观锁冲突：请求带的 version 与库里当前值不一致。
// service.ToStatus 把它映射成 Aborted / 409。
var ErrVersionConflict = errors.New("version 冲突：与库里当前值不一致")

// ErrNotFound：按 id 查不到。映射成 NotFound / 404。
var ErrNotFound = errors.New("not found")

// customerColumns 是 scanCustomerRow 期望的列顺序，所有 SELECT / RETURNING
// 都用它，免得列序与 Scan 的参数序对不上。
const customerColumns = `id, code, name, tax_no, credit_limit, created_at, updated_at, version, status`

// rowScanner 是 *sql.Row 与 *sql.Rows 的公共部分。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanCustomerRow(row rowScanner) (*Customer, error) {
	var rawID int64
	var c Customer
	if err := row.Scan(&rawID, &c.Code, &c.Name, &c.TaxNo, &c.CreditLimit,
		&c.CreatedAt, &c.UpdatedAt, &c.Version, &c.Status); err != nil {
		return nil, err
	}
	c.ID = strconv.FormatInt(rawID, 10)
	return &c, nil
}

// scanCustomer 是 besdk.BatchGetRouted 要的扫描函数形状。
func scanCustomer(rows *sql.Rows) (string, *Customer, error) {
	c, err := scanCustomerRow(rows)
	if err != nil {
		return "", nil, err
	}
	return c.ID, c, nil
}

func getByID(ctx context.Context, tx *sql.Tx, id string) (*Customer, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+customerColumns+` FROM customers WHERE id = $1`, id)
	c, err := scanCustomerRow(row)
	if err != nil {
		return nil, fmt.Errorf("查 customers: %w", err)
	}
	return c, nil
}

func getContactByID(ctx context.Context, tx *sql.Tx, id string) (*Contact, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, name, phone, email, is_primary FROM contacts WHERE id = $1`, id)
	var rawID int64
	var c Contact
	if err := row.Scan(&rawID, &c.Name, &c.Phone, &c.Email, &c.Primary); err != nil {
		return nil, fmt.Errorf("查 contacts: %w", err)
	}
	c.ID = strconv.FormatInt(rawID, 10)
	return &c, nil
}
