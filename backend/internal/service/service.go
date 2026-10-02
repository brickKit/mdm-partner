// Package service 是 mdm-customer 的业务规则层。这一层薄：乐观锁、幂等、事件
// 都在 repo 层随 SQL 在同一个事务里做；这里做入参校验，给 http / grpc 一个
// 共用的入口，外加错误日志。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

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

// validateCreditLimit 只校验"是合法的非负数"，精度交给 NUMERIC(18,2) 自己报错。
// 解析成 float64 只为这一次校验，金额的存储与传递一律是十进制字符串。
func validateCreditLimit(s string) error {
	if s == "" {
		return nil // repo 层留空时默认成 "0"
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("%w: credit_limit 不是合法数字：%q", ErrInvalidArgument, s)
	}
	if f < 0 {
		return fmt.Errorf("%w: credit_limit 不能为负数：%q", ErrInvalidArgument, s)
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
		s.logger.Error("创建客户失败", "code", in.Code, "error", err)
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
		s.logger.Error("更新客户失败", "id", in.ID, "error", err)
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
	c, err := s.repo.SetStatus(ctx, repo.SetStatusInput{
		IdempotencyKey: in.IdempotencyKey,
		ID:             in.ID,
		Version:        in.Version,
		Status:         in.Status,
	})
	if err != nil {
		s.logger.Error("变更客户状态失败", "id", in.ID, "status", in.Status, "error", err)
		return nil, err
	}
	return c, nil
}

func (s *Service) AddContact(ctx context.Context, in repo.AddContactInput) (*repo.Contact, error) {
	c, err := s.repo.AddContact(ctx, in)
	if err != nil {
		s.logger.Error("新增联系人失败", "customer_id", in.CustomerID, "error", err)
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
