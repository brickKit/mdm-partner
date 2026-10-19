package partner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
)

func (m *Module) listRoles(c *httpx.Context) error {
	ctx, _, err := check(c, OpRead)
	if err != nil {
		return err
	}
	p, err := m.partner(ctx, c.Param("id"))
	if err != nil {
		return err
	}
	out := []Role{}
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+roleCols+` FROM partner_role WHERE partner_id = $1 ORDER BY role, legal_entity_id`, p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRole(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"roles": out})
}

// legalEntityActive asks mdm/org now, outside any transaction, whether the legal entity exists and is active. When
// mdm/org cannot answer, the caller cannot prove it and refuses: unreachable is never "does not exist" (protocol 07).
func (m *Module) legalEntityActive(ctx context.Context, id string) error {
	conn, ok := m.rt.GRPC(Org)
	if !ok {
		return errs.New(errs.ReasonDependencyUnavailable, "mdm/org is not reachable: the legal entity cannot be checked")
	}
	res, err := orgv3.NewSourceServiceClient(conn).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_LEGAL_ENTITY, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return errLegalEntity.New("no legal entity " + id)
	}
	var le struct{ State string }
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &le); err != nil {
		return err
	}
	if le.State != "active" {
		return errLegalEntity.New("the legal entity is " + le.State)
	}
	return nil
}

type roleIn struct {
	DefaultCurrency *string `json:"default_currency"`
	PaymentTerms    *string `json:"payment_terms"`
}

// putCustomerRole opens the customer role in a legal entity (which mdm/org says is active now) or changes its
// defaults; the same content again changes nothing. A retired role stays retired.
func (m *Module) putCustomerRole(c *httpx.Context) error {
	ctx, actor, err := check(c, OpRole)
	if err != nil {
		return err
	}
	pid, le := c.Param("id"), c.Param("legal_entity_id")
	if !uuidForm.MatchString(pid) {
		return notFound("partner")
	}
	if !uuidForm.MatchString(le) {
		return invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	}
	var in roleIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if err := checkCurrency(in.DefaultCurrency); err != nil {
		return err
	}
	if in.PaymentTerms != nil && (*in.PaymentTerms == "" || len(*in.PaymentTerms) > 32) {
		return invalid("payment_terms", "payment_terms is a code of at most 32, or null")
	}
	if err := m.legalEntityActive(ctx, le); err != nil { // before the transaction: no network calls inside one
		return err
	}
	var out Role
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		p, err := getPartner(ctx, tx, pid, true)
		if err != nil {
			return err
		}
		if p.State != "active" {
			return errState.New("the partner is " + p.State + ": no roles are opened or changed")
		}
		cur, err := getRole(ctx, tx, pid, "customer", le, true)
		switch {
		case errors.Is(err, errNone):
			id := ids.New().String()
			if _, err := tx.Exec(ctx, `INSERT INTO partner_role (id, partner_id, role, legal_entity_id, default_currency,
				payment_terms) VALUES ($1, $2, 'customer', $3, $4, $5)`, id, pid, le, in.DefaultCurrency, in.PaymentTerms); err != nil {
				return err
			}
		case err != nil:
			return err
		case cur.State == "retired":
			return errState.New("a retired role stays retired")
		case same(cur.DefaultCurrency, in.DefaultCurrency) && same(cur.PaymentTerms, in.PaymentTerms):
			out = cur
			return nil
		default:
			if _, err := tx.Exec(ctx, `UPDATE partner_role SET default_currency = $2, payment_terms = $3,
				version = version + 1, updated_at = now() WHERE id = $1`, cur.ID, in.DefaultCurrency, in.PaymentTerms); err != nil {
				return err
			}
		}
		if out, err = getRole(ctx, tx, pid, "customer", le, false); err != nil {
			return err
		}
		if err := m.emit(ctx, tx, RoleChanged, out.ID, out.Version, out); err != nil {
			return err
		}
		return m.change(ctx, tx, actor, OpRole, Roles, out.ID, out)
	})
	if errors.Is(err, errNone) {
		return notFound("partner")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

// customerRoleState suspends, resumes or retires the customer role; a retired role stays retired.
func (m *Module) customerRoleState(c *httpx.Context) error {
	ctx, actor, err := check(c, OpRole)
	if err != nil {
		return err
	}
	pid, le := c.Param("id"), c.Param("legal_entity_id")
	if !uuidForm.MatchString(pid) || !uuidForm.MatchString(le) {
		return notFound("role")
	}
	var in struct {
		State   string `json:"state"`
		Version *int64 `json:"version"`
	}
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.State != "active" && in.State != "suspended" && in.State != "retired" {
		return invalid("state", "state is active, suspended or retired")
	}
	var out Role
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		cur, err := getRole(ctx, tx, pid, "customer", le, true)
		if err != nil {
			return err
		}
		if in.Version != nil && *in.Version != cur.Version {
			return errVersion.New(fmt.Sprintf("the role is at version %d", cur.Version))
		}
		if cur.State == in.State {
			out = cur
			return nil
		}
		if cur.State == "retired" {
			return errState.New("a retired role stays retired")
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_role SET state = $2, version = version + 1, updated_at = now()
			WHERE id = $1`, cur.ID, in.State); err != nil {
			return err
		}
		if out, err = getRole(ctx, tx, pid, "customer", le, false); err != nil {
			return err
		}
		if err := m.emit(ctx, tx, RoleChanged, out.ID, out.Version, out); err != nil {
			return err
		}
		return m.change(ctx, tx, actor, OpRole, Roles, out.ID, out)
	})
	if errors.Is(err, errNone) {
		return notFound("role")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

// Eligibility is protocol 07's evidence for a new reference: eligible only with an active partner and an active role
// of the purpose in the legal entity; the reason says which part is missing.
type Eligibility struct {
	Eligible       bool   `json:"eligible"`
	Reason         string `json:"reason"`
	PartnerVersion int64  `json:"partner_version,omitempty"`
	RoleID         string `json:"role_id,omitempty"`
	RoleVersion    int64  `json:"role_version,omitempty"`
	SurvivorID     string `json:"survivor_id,omitempty"`
	EvaluatedAt    string `json:"evaluated_at"`
}

// roleOf is the role a purpose needs; ok=false: a purpose of a role not in this version.
func roleOf(purpose string) (string, bool) {
	switch purpose {
	case "sell":
		return "customer", true
	case "purchase", "pay_employee":
		return "", false
	}
	return "", false
}

func (m *Module) eligibility(ctx context.Context, partnerID, legalEntity, purpose string) (Eligibility, error) {
	role, ok := roleOf(purpose)
	if !ok {
		if purpose == "purchase" || purpose == "pay_employee" {
			return Eligibility{}, errs.New(errs.ReasonCapabilityUnsupported, "the "+purpose+" purpose needs a role not in this version")
		}
		return Eligibility{}, invalid("purpose", "purpose is sell, purchase or pay_employee")
	}
	e := Eligibility{EvaluatedAt: canon.Time(time.Now())}
	if !uuidForm.MatchString(partnerID) || !uuidForm.MatchString(legalEntity) {
		e.Reason = "not_found"
		return e, nil
	}
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		p, err := getPartner(ctx, tx, partnerID, false)
		if errors.Is(err, errNone) {
			e.Reason = "not_found"
			return nil
		}
		if err != nil {
			return err
		}
		e.PartnerVersion = p.Version
		if p.State != "active" {
			e.Reason = "partner_" + p.State
			return nil
		}
		r, err := getRole(ctx, tx, partnerID, role, legalEntity, false)
		if errors.Is(err, errNone) {
			e.Reason = "role_not_opened"
			return nil
		}
		if err != nil {
			return err
		}
		e.RoleID, e.RoleVersion = r.ID, r.Version
		if r.State != "active" {
			e.Reason = "role_" + r.State
			return nil
		}
		e.Eligible, e.Reason = true, "eligible"
		return nil
	})
	return e, err
}

func (m *Module) eligibilityHTTP(c *httpx.Context) error {
	ctx, _, err := check(c, OpRead)
	if err != nil {
		return err
	}
	e, err := m.eligibility(ctx, c.Query("partner_id"), c.Query("legal_entity_id"), c.Query("purpose"))
	if err != nil {
		return err
	}
	return c.JSON(200, e)
}
