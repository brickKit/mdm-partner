package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
			// 只有调用方自己传了 code，撞唯一索引才是它的错（409）；自动编号的撞车
			// 在 insertAutoCode 里顺延，顺延耗尽是服务端故障（Internal），不翻译。
			if in.Code != "" {
				return classifyWriteErr(err)
			}
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
	c := &Customer{Code: in.Code, Name: in.Name, TaxNo: in.TaxNo, CreditLimit: in.CreditLimit, Status: "ACTIVE"}
	if c.CreditLimit == "" {
		c.CreditLimit = "0"
	}
	if c.Code == "" {
		if err := insertAutoCode(ctx, tx, c); err != nil {
			return nil, err
		}
		return c, nil
	}
	// 显式编号：重复时由唯一索引 customers_code_uniq 报错，Create 把它翻成 ErrCodeTaken。
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO customers (code, name, tax_no, credit_limit)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at, version, credit_limit`,
		c.Code, c.Name, c.TaxNo, c.CreditLimit,
	).Scan(&id, &c.CreatedAt, &c.UpdatedAt, &c.Version, &c.CreditLimit); err != nil {
		return nil, fmt.Errorf("insert customers: %w", err)
	}
	c.ID = strconv.FormatInt(id, 10)
	return c, nil
}

// autoCodeAttempts 是自动编号撞车时最多试几个序列值。撞车只在显式 code 占了序列将来
// 要发的值时发生，正常一次就成；连续 5 个都被占，说明有人成片导入了自动编号形状的
// code，再顺延下去只是在扫别人的编号区，交给运维（Internal + ERROR）。
const autoCodeAttempts = 5

// insertAutoCode 是不带 code 的 Create：一次 nextval 同时决定 id 与 code（"C" + 6 位），
// 插入时显式给 id，不必插入之后再回改 code。成功时填好 c 的 ID、Code 与落库后的列。
//
// 自动编号与显式 code 共用一个编号空间：显式 code 可以是 "C000123" 这种形状（从旧系统
// 导入、重新导入本系统导出的编号），序列走到这个值时就撞上 customers_code_uniq。这是
// 服务端的编号撞车，不是调用方的错——调用方没传 code，不能答 409 "code 已被占用"。所以
// 用 ON CONFLICT (code) DO NOTHING：撞车时一行不插、事务不进入出错状态（不用保存点），
// 取下一个序列值再试；序列不随事务回滚，跳过的值不会再发。
func insertAutoCode(ctx context.Context, tx *sql.Tx, c *Customer) error {
	tried := make([]string, 0, autoCodeAttempts)
	for attempt := 0; attempt < autoCodeAttempts; attempt++ {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT nextval('customers_id_seq')`).Scan(&id); err != nil {
			return fmt.Errorf("生成客户编号: %w", err)
		}
		code := fmt.Sprintf("C%06d", id)
		err := tx.QueryRowContext(ctx, `
			INSERT INTO customers (id, code, name, tax_no, credit_limit)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (code) DO NOTHING
			RETURNING created_at, updated_at, version, credit_limit`,
			id, code, c.Name, c.TaxNo, c.CreditLimit,
		).Scan(&c.CreatedAt, &c.UpdatedAt, &c.Version, &c.CreditLimit)
		switch {
		case err == nil:
			c.ID, c.Code = strconv.FormatInt(id, 10), code
			return nil
		case err == sql.ErrNoRows: // 这个编号已被显式 code 占用
			tried = append(tried, code)
		default:
			return fmt.Errorf("insert customers: %w", err)
		}
	}
	return fmt.Errorf("自动编号连续 %d 次撞上已被显式占用的 code（%s）：显式 code 占了序列将来要发的编号区，需要运维处理",
		autoCodeAttempts, strings.Join(tried, "、"))
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
	return r.updateWithVersion(ctx, in.IdempotencyKey, "Update", in.ID, `
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
	return r.updateWithVersion(ctx, in.IdempotencyKey, "SetStatus", in.ID, `
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
// （WHERE version = 请求值）+ 幂等键 + 事件。没有行被改时再查一次客户在不在：
// 不在是 ErrNotFound（404），在才是版本冲突（409）。不是数字的 id 直接 ErrNotFound。
func (r *Repo) updateWithVersion(ctx context.Context, key, command, id, query string, args []any,
	subjectOf func(*Customer) string) (*Customer, error) {
	var out *Customer
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		if c, ok, err := replayCustomer(ctx, tx, key); err != nil || ok {
			out = c
			return err
		}
		if !isNumericID(id) {
			return ErrNotFound
		}
		c, err := scanCustomerRow(tx.QueryRowContext(ctx, query, args...))
		if err == sql.ErrNoRows {
			return missingOrConflict(ctx, tx, id)
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

// missingOrConflict 区分带 version 的 UPDATE 没命中的两种原因。
func missingOrConflict(ctx context.Context, tx *sql.Tx, id string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("查 customers: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrVersionConflict
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

		if !isNumericID(in.CustomerID) {
			return ErrNotFound
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO contacts (customer_id, name, phone, email, is_primary)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`,
			in.CustomerID, in.Name, in.Phone, in.Email, in.Primary,
		).Scan(&id); err != nil {
			return classifyWriteErr(fmt.Errorf("insert contacts: %w", err))
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
