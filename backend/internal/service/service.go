// Package service 是 mdm-customer 的业务规则层。这一层薄：乐观锁、幂等、事件
// 都在 repo 层随 SQL 在同一个事务里做；这里做入参校验，给 http / grpc 一个
// 共用的入口，外加错误日志（级别按错误性质选，见 logFailure）。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
)

// ErrInvalidArgument 是入参本身不合法（不是数据库层面的冲突/缺失），
// grpc/http 两层都通过 ToStatus 把它映射成 InvalidArgument/400。这一层
// 是 http/grpc 唯一共用的入口，校验只写这一处，两个协议都不用各写一遍。
var ErrInvalidArgument = errors.New("参数不合法")

type Service struct {
	repo   *repo.Repo
	logger *slog.Logger
}

func New(r *repo.Repo, logger *slog.Logger) *Service {
	return &Service{repo: r, logger: logger}
}

// logFailure 按错误的性质选日志级别。调用方自己能纠正的错误（参数、重复 code、
// 找不到、版本过期）记 Info：访问日志与 RED 指标已经记下状态码。调用方断开或进程
// 关停时的取消记 Warn。ERROR 只留给映射成 Internal、需要运维处理的错误。
func (s *Service) logFailure(msg string, err error, attrs ...any) {
	attrs = append(attrs, "error", err)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.logger.Warn(msg, attrs...)
	case status.Code(ToStatus(err)) == codes.Internal:
		s.logger.Error(msg, attrs...)
	default:
		s.logger.Info(msg, attrs...)
	}
}

// decimalCreditLimit 是 credit_limit 唯一接受的写法：非负十进制字符串，最多 16 位
// 整数、2 位小数——与列类型 NUMERIC(18,2) 对齐，超出的位数不会被悄悄舍入或溢出成 500。
//
// ⚠️ 不能用 strconv.ParseFloat 校验：它放行 "NaN"、"Inf"、"1e3"、"0x1p4"。NaN 存进
// NUMERIC 排在所有数之上，随 created / updated 事件流进 erp/sales 与 erp/finance，
// 拿它当额度上限的检查对这个客户从此恒真。
var decimalCreditLimit = regexp.MustCompile(`^[0-9]{1,16}(\.[0-9]{1,2})?$`)

// validateCreditLimit 只看字符串，不转浮点。空串放行：repo 层留空时默认成 "0"。
// 金额的存储与传递一律是十进制字符串。
func validateCreditLimit(s string) error {
	if s == "" {
		return nil
	}
	if !decimalCreditLimit.MatchString(s) {
		return fmt.Errorf("%w: credit_limit 必须是非负十进制数（最多 16 位整数、2 位小数；不收 NaN / Inf / 科学计数法）：%q",
			ErrInvalidArgument, s)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in repo.CreateInput) (*repo.Customer, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name 不能为空", ErrInvalidArgument)
	}
	if err := validateCreditLimit(in.CreditLimit); err != nil {
		return nil, err
	}
	c, err := s.repo.Create(ctx, in)
	if err != nil {
		s.logFailure("创建客户失败", err, "code", in.Code)
		return nil, err
	}
	return c, nil
}

type UpdateInput struct {
	IdempotencyKey string
	ID             string
	Version        int64
	Name           string
	TaxNo          string
	CreditLimit    string
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (*repo.Customer, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name 不能为空", ErrInvalidArgument)
	}
	if err := validateCreditLimit(in.CreditLimit); err != nil {
		return nil, err
	}
	c, err := s.repo.Update(ctx, repo.UpdateInput{
		IdempotencyKey: in.IdempotencyKey,
		ID:             in.ID,
		Version:        in.Version,
		Name:           in.Name,
		TaxNo:          in.TaxNo,
		CreditLimit:    in.CreditLimit,
	})
	if err != nil {
		s.logFailure("更新客户失败", err, "id", in.ID)
		return nil, err
	}
	return c, nil
}

type SetStatusInput struct {
	IdempotencyKey string
	ID             string
	Version        int64
	Status         string
}

func (s *Service) SetStatus(ctx context.Context, in SetStatusInput) (*repo.Customer, error) {
	if in.Status != "ACTIVE" && in.Status != "DISABLED" {
		return nil, fmt.Errorf("%w: status 只能是 ACTIVE 或 DISABLED：%q", ErrInvalidArgument, in.Status)
	}
	c, err := s.repo.SetStatus(ctx, repo.SetStatusInput{
		IdempotencyKey: in.IdempotencyKey,
		ID:             in.ID,
		Version:        in.Version,
		Status:         in.Status,
	})
	if err != nil {
		s.logFailure("变更客户状态失败", err, "id", in.ID, "status", in.Status)
		return nil, err
	}
	return c, nil
}

func (s *Service) AddContact(ctx context.Context, in repo.AddContactInput) (*repo.Contact, error) {
	c, err := s.repo.AddContact(ctx, in)
	if err != nil {
		s.logFailure("新增联系人失败", err, "customer_id", in.CustomerID)
		return nil, err
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, id string) (*repo.Customer, error) {
	got, _, err := s.repo.BatchGet(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, repo.ErrNotFound
	}
	return got[0], nil
}

func (s *Service) BatchGet(ctx context.Context, ids []string) (found []*repo.Customer, missing []string, err error) {
	return s.repo.BatchGet(ctx, ids)
}

func (s *Service) List(ctx context.Context, in repo.ListInput) (*repo.ListResult, error) {
	return s.repo.List(ctx, in)
}
