// Package http 是 mdm-customer 的 REST 面（路径前缀 /mdm/customer，与
// assembly.yaml 的 edge_routes 一致）。/healthz、/metrics 由
// besdk.NewGinEngine 挂好，这里不重复挂，也不写进 openapi（契约以
// /mdm/customer 为前缀，写进去会暗示错误的路径）。
//
// 返回的 Customer 不含 contacts / billing_infos 数组：AddContact 写得进去，
// 读路径还没有联查（docs/design.md 的未决问题）。
package http

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/v2/backend/internal/repo"
	"github.com/brickKit/mdm-customer/v2/backend/internal/service"
)

// RegisterRoutes 挂载业务路由。每条路由都带 assembly.yaml 里声明的权限键
// 注册（besdk.GET / POST / PATCH），没有不鉴权的业务路由。data_scopes: none：
// 客户主数据全员可见，不做行级过滤。
func RegisterRoutes(eng *gin.Engine, svc *service.Service) {
	g := eng.Group("/mdm/customer")
	besdk.GET(g, "/customers", "mdm.customer.view", listHandler(svc))
	besdk.GET(g, "/customers/:id", "mdm.customer.view", getHandler(svc))
	besdk.POST(g, "/customers", "mdm.customer.create", createHandler(svc))
	besdk.PATCH(g, "/customers/:id", "mdm.customer.update", updateHandler(svc))
	besdk.POST(g, "/customers/:id/status", "mdm.customer.set_status", setStatusHandler(svc))
	besdk.POST(g, "/customers/:id/contacts", "mdm.customer.add_contact", addContactHandler(svc))
}

type customerDTO struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	TaxNo       string `json:"tax_no"`
	CreditLimit string `json:"credit_limit"`
	Status      string `json:"status"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toDTO(c *repo.Customer) customerDTO {
	return customerDTO{
		ID: c.ID, Code: c.Code, Name: c.Name, TaxNo: c.TaxNo,
		CreditLimit: c.CreditLimit, Status: c.Status, Version: c.Version,
		CreatedAt: c.CreatedAt.Format(rfc3339), UpdatedAt: c.UpdatedAt.Format(rfc3339),
	}
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

type createRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Code           string `json:"code"`
	Name           string `json:"name" binding:"required"`
	TaxNo          string `json:"tax_no"`
	CreditLimit    string `json:"credit_limit"`
}

func createHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.Create(c.Request.Context(), repo.CreateInput{
			IdempotencyKey: req.IdempotencyKey, Code: req.Code, Name: req.Name,
			TaxNo: req.TaxNo, CreditLimit: req.CreditLimit,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

func getHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		out, err := svc.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

// parseListInput 读 GET /customers 的查询参数。q 是关键字（匹配规则见
// repo.List）；created_after / created_before
// 是 RFC 3339 时间，不传就由 repo 补默认窗口；格式不对返回错误（handler
// 回 400），不悄悄忽略。
func parseListInput(c *gin.Context) (repo.ListInput, error) {
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	in := repo.ListInput{
		Q:            c.Query("q"),
		Cursor:       c.Query("cursor"),
		PageSize:     pageSize,
		StatusFilter: c.Query("status_filter"),
	}
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"created_after", &in.CreatedAfter}, {"created_before", &in.CreatedBefore}} {
		raw := c.Query(p.name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return in, fmt.Errorf("%s 不是 RFC 3339 时间：%q", p.name, raw)
		}
		*p.dst = t
	}
	return in, nil
}

func listHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		in, err := parseListInput(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.List(c.Request.Context(), in)
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]customerDTO, 0, len(out.Customers))
		for _, cust := range out.Customers {
			dtos = append(dtos, toDTO(cust))
		}
		c.JSON(http.StatusOK, gin.H{"customers": dtos, "next_cursor": out.NextCursor})
	}
}

type updateRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Version        int64  `json:"version" binding:"required"`
	Name           string `json:"name"`
	TaxNo          string `json:"tax_no"`
	CreditLimit    string `json:"credit_limit"`
}

func updateHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req updateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.Update(c.Request.Context(), service.UpdateInput{
			IdempotencyKey: req.IdempotencyKey, ID: c.Param("id"), Version: req.Version,
			Name: req.Name, TaxNo: req.TaxNo, CreditLimit: req.CreditLimit,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

type setStatusRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Version        int64  `json:"version" binding:"required"`
	Status         string `json:"status" binding:"required"`
}

func setStatusHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req setStatusRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.SetStatus(c.Request.Context(), service.SetStatusInput{
			IdempotencyKey: req.IdempotencyKey, ID: c.Param("id"),
			Version: req.Version, Status: req.Status,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

type addContactRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Contact        struct {
		Name    string `json:"name" binding:"required"`
		Phone   string `json:"phone"`
		Email   string `json:"email"`
		Primary bool   `json:"primary"`
	} `json:"contact" binding:"required"`
}

func addContactHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req addContactRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.AddContact(c.Request.Context(), repo.AddContactInput{
			IdempotencyKey: req.IdempotencyKey, CustomerID: c.Param("id"),
			Name: req.Contact.Name, Phone: req.Contact.Phone,
			Email: req.Contact.Email, Primary: req.Contact.Primary,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id": out.ID, "name": out.Name, "phone": out.Phone,
			"email": out.Email, "primary": out.Primary,
		})
	}
}
