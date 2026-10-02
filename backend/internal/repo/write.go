package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// 四个写命令共用同一个形状，全部在一个 besdk.WithTx 事务里：
//  1. 查 command_idempotency：同一个 idempotency_key 已经执行过，就原样返回
//     上次的结果，不再写任何东西（网络重试、前端重复提交都落在这里）；
//  2. 写业务行；
//  3. 记幂等键——idempotency_key 是主键，两个并发的同键请求里后提交的那个
//     在这里撞主键、整个事务回滚，所以最终只落一条（它自己会收到错误）；
//  4. 写 Outbox（与业务行同事务：任一步失败，事件与业务行一起回滚）。

// testHookAfterOutbox 只给本包测试用：Create 在写完 Outbox、提交之前调它，
// 测试借此验证"业务数据与事件同事务"，生产代码里永远是 nil。
var testHookAfterOutbox func() error

// lookupIdempotency 返回空字符串表示没查到（不是错误）。
func lookupIdempotency(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var resultID string
	err := tx.QueryRowContext(ctx,
		`SELECT result_id FROM command_idempotency WHERE idempotency_key = $1`, key).Scan(&resultID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("查 command_idempotency: %w", err)
	}
	return resultID, nil
}

// replayCustomer 是第 1 步：幂等键命中时返回上次的客户（ok=true）。
func replayCustomer(ctx context.Context, tx *sql.Tx, key string) (*Customer, bool, error) {
	existingID, err := lookupIdempotency(ctx, tx, key)
	if err != nil || existingID == "" {
		return nil, false, err
	}
	c, err := getByID(ctx, tx, existingID)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// recordIdempotency 是第 3 步。
func recordIdempotency(ctx context.Context, tx *sql.Tx, key, command, resultID string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO command_idempotency (idempotency_key, command, result_id) VALUES ($1, $2, $3)`,
		key, command, resultID); err != nil {
		return fmt.Errorf("insert command_idempotency: %w", err)
	}
	return nil
}

// publish 是第 4 步。
func (r *Repo) publish(tx *sql.Tx, subject, aggregateID string, version int64, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, r.schema, besdk.Event{
		Subject: subject, AggregateID: aggregateID, Version: version, Payload: raw,
	})
}

// changePayload 是 updated / disabled 事件的 payload（created 另外带 code）。
func changePayload(c *Customer) map[string]any {
	return map[string]any{
		"id": c.ID, "name": c.Name, "tax_no": c.TaxNo,
		"credit_limit": c.CreditLimit, "status": c.Status, "version": c.Version,
	}
}

// CreateInput 对应 CreateRequest（contracts/mdm/customer/v1/customer.proto）。
type CreateInput struct {
	IdempotencyKey string
	Code           string // 留空则自动生成 "C" + 6 位数字
	Name           string
	TaxNo          string
	CreditLimit    string
}

// Create 新建客户，发 mdm.customer.created.v1。
func (r *Repo) Create(ctx context.Context, in CreateInput) (*Customer, error) {
	var out *Customer
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		if c, ok, err := replayCustomer(ctx, tx, in.IdempotencyKey); err != nil || ok {
			out = c
			return err
		}

		c, err := insertCustomer(ctx, tx, in)
		if err != nil {
			return err
		}
		if err := recordIdempotency(ctx, tx, in.IdempotencyKey, "Create", c.ID); err != nil {
			return err
		}
		if err := r.publish(tx, "mdm.customer.created.v1", c.ID, c.Version, map[string]any{
			"id": c.ID, "code": c.Code, "name": c.Name, "tax_no": c.TaxNo,
			"credit_limit": c.CreditLimit, "status": c.Status, "version": c.Version,
		}); err != nil {
			return err
		}
		if testHookAfterOutbox != nil {
			if err := testHookAfterOutbox(); err != nil {
				return err
			}
		}
		out = c
		return nil
	})
	return out, err
}

// insertCustomer 写 customers 一行并返回落库后的值。
//
// credit_limit 一定从 RETURNING 读回，不回填调用方传入的原文：NUMERIC(18,2)
// 会把 "0" 规整成 "0.00"，回填原文会让 Create 的返回值与随后 Get 同一条
// 记录读到的格式不一致。
func insertCustomer(ctx context.Context, tx *sql.Tx, in CreateInput) (*Customer, error) {
	creditLimit := in.CreditLimit
	if creditLimit == "" {
		creditLimit = "0"
	}
	var id int64
	code := in.Code
	var createdAt, updatedAt time.Time
	var version int64

	if code == "" {
		// 自动编号：一次 nextval 同时决定 id 与 code，插入时显式给 id，
		// 不必插入之后再回改 code。
		if err := tx.QueryRowContext(ctx, `SELECT nextval('customers_id_seq')`).Scan(&id); err != nil {
			return nil, fmt.Errorf("生成客户编号: %w", err)
		}
		code = fmt.Sprintf("C%06d", id)
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO customers (id, code, name, tax_no, credit_limit)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING created_at, updated_at, version, credit_limit`,
			id, code, in.Name, in.TaxNo, creditLimit,
		).Scan(&createdAt, &updatedAt, &version, &creditLimit); err != nil {
			return nil, fmt.Errorf("insert customers: %w", err)
		}
	} else {
		// 显式编号：重复时由唯一索引 customers_code_uniq 报错。
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO customers (code, name, tax_no, credit_limit)
			VALUES ($1, $2, $3, $4)
			RETURNING id, created_at, updated_at, version, credit_limit`,
			code, in.Name, in.TaxNo, creditLimit,
		).Scan(&id, &createdAt, &updatedAt, &version, &creditLimit); err != nil {
			return nil, fmt.Errorf("insert customers: %w", err)
		}
	}
	return &Customer{
		ID: strconv.FormatInt(id, 10), Code: code, Name: in.Name, TaxNo: in.TaxNo,
		CreditLimit: creditLimit, Status: "ACTIVE", Version: version,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

// UpdateInput 对应 UpdateRequest。
type UpdateInput struct {
	IdempotencyKey string
	ID             string
	Version        int64 // 乐观锁：与库里不一致则拒绝
	Name           string
	TaxNo          string
	CreditLimit    string
}

// Update 改名称、税号、信用额度，发 mdm.customer.updated.v1。
func (r *Repo) Update(ctx context.Context, in UpdateInput) (*Customer, error) {
	creditLimit := in.CreditLimit
	if creditLimit == "" {
		creditLimit = "0"
	}
	return r.updateWithVersion(ctx, in.IdempotencyKey, "Update", `
		UPDATE customers
		SET name = $1, tax_no = $2, credit_limit = $3, version = version + 1, updated_at = now()
		WHERE id = $4 AND version = $5
		RETURNING `+customerColumns,
		[]any{in.Name, in.TaxNo, creditLimit, in.ID, in.Version},
		func(*Customer) string { return "mdm.customer.updated.v1" })
}

// SetStatusInput 对应 SetStatusRequest。
//
// DISABLED 不是终态：允许流转回 ACTIVE（客户可能重新建立业务关系），两个
// 方向都只受乐观锁约束。
type SetStatusInput struct {
	IdempotencyKey string
	ID             string
	Version        int64
	Status         string // "ACTIVE" | "DISABLED"
}

// SetStatus 启用 / 停用。停用发 mdm.customer.disabled.v1；重新启用发
// mdm.customer.updated.v1——事件契约里没有 "enabled" 这个 subject，updated
// 的 payload 本来就带 status，新增 subject 要先改契约。
func (r *Repo) SetStatus(ctx context.Context, in SetStatusInput) (*Customer, error) {
	return r.updateWithVersion(ctx, in.IdempotencyKey, "SetStatus", `
		UPDATE customers
		SET status = $1, version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
		RETURNING `+customerColumns,
		[]any{in.Status, in.ID, in.Version},
		func(c *Customer) string {
			if c.Status == "DISABLED" {
				return "mdm.customer.disabled.v1"
			}
			return "mdm.customer.updated.v1"
		})
}

// updateWithVersion 是 Update 与 SetStatus 共用的事务：带乐观锁的 UPDATE
// （WHERE version = 请求值，没有行被改就是版本冲突）+ 幂等键 + 事件。
func (r *Repo) updateWithVersion(ctx context.Context, key, command, query string, args []any,
	subjectOf func(*Customer) string) (*Customer, error) {
	var out *Customer
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		if c, ok, err := replayCustomer(ctx, tx, key); err != nil || ok {
			out = c
			return err
		}
		c, err := scanCustomerRow(tx.QueryRowContext(ctx, query, args...))
		if err == sql.ErrNoRows {
			return ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update customers: %w", err)
		}
		if err := recordIdempotency(ctx, tx, key, command, c.ID); err != nil {
			return err
		}
		if err := r.publish(tx, subjectOf(c), c.ID, c.Version, changePayload(c)); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// AddContactInput 对应 AddContactRequest。
type AddContactInput struct {
	IdempotencyKey string
	CustomerID     string
	Name           string
	Phone          string
	Email          string
	Primary        bool
}

// AddContact 给客户加一个联系人。没有对应的事件：事件契约里没有"联系人
// 新增"这个 subject，新增 subject 要先改契约。
func (r *Repo) AddContact(ctx context.Context, in AddContactInput) (*Contact, error) {
	var out *Contact
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		existingID, err := lookupIdempotency(ctx, tx, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if existingID != "" {
			out, err = getContactByID(ctx, tx, existingID)
			return err
		}

		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO contacts (customer_id, name, phone, email, is_primary)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`,
			in.CustomerID, in.Name, in.Phone, in.Email, in.Primary,
		).Scan(&id); err != nil {
			return fmt.Errorf("insert contacts: %w", err)
		}
		idStr := strconv.FormatInt(id, 10)
		if err := recordIdempotency(ctx, tx, in.IdempotencyKey, "AddContact", idStr); err != nil {
			return err
		}
		out = &Contact{ID: idStr, Name: in.Name, Phone: in.Phone, Email: in.Email, Primary: in.Primary}
		return nil
	})
	return out, err
}
