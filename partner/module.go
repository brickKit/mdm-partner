package partner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/brickKit/be-sdk-go/v3/audit"
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/identity"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/search"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
)

// The component's own reasons (the SDK's catalog has the shared ones).
var (
	errVersion = errs.Define(ID, "VERSION_CONFLICT", errs.Aborted, 409, errs.RetryNo, "The record changed since that version")
	errState   = errs.Define(ID, "STATE_CONFLICT", errs.FailedPrecondition, 409, errs.RetryNo,
		"The record's state does not allow this")
	errLegalEntity = errs.Define(ID, "LEGAL_ENTITY_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The legal entity does not exist or is not active")
)

var (
	uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	country  = regexp.MustCompile(`^[A-Z]{2}$`)
	isoCcy   = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Module is the partner component.
type Module struct {
	partnerv3.UnimplementedSourceServiceServer
	partnerv3.UnimplementedPartnerServiceServer
	rt                   *runtime.Runtime
	deployment, instance string
}

// New builds the module from its runtime.
func New(rt *runtime.Runtime) (*Module, error) {
	return &Module{rt: rt, deployment: rt.Config.String("DEPLOYMENT_ID"), instance: rt.Config.String("IMPLEMENTATION_INSTANCE")}, nil
}

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h})
	}
	add("GET", "/api/partner/partners", OpRead, m.listPartners)
	add("POST", "/api/partner/partners", OpCreate, m.createPartner)
	add("GET", "/api/partner/partners/:id", OpRead, m.getPartner)
	add("PATCH", "/api/partner/partners/:id", OpEdit, m.updatePartner)
	add("POST", "/api/partner/partners/:id/state", OpEdit, m.partnerState)
	add("GET", "/api/partner/partners/:id/roles", OpRead, m.listRoles)
	add("PUT", "/api/partner/partners/:id/roles/customer/:legal_entity_id", OpRole, m.putCustomerRole)
	add("POST", "/api/partner/partners/:id/roles/customer/:legal_entity_id/state", OpRole, m.customerRoleState)
	add("GET", "/api/partner/eligibility", OpRead, m.eligibilityHTTP)
	// in the contract, not in step 6 (D75)
	for _, u := range []struct{ method, path, op string }{
		{"PUT", "/api/partner/partners/:id/roles/supplier/:legal_entity_id", OpRole},
		{"PUT", "/api/partner/partners/:id/roles/employee/:legal_entity_id", OpRole},
		{"POST", "/api/partner/partners/:id/merge", OpMerge},
		{"POST", "/api/partner/partners/:id/split", OpMerge},
		{"GET", "/api/partner/partners/:id/bank-accounts", OpRead},
		{"POST", "/api/partner/partners/:id/bank-accounts", OpEdit},
	} {
		add(u.method, u.path, u.op, unsupported)
	}
	return nil
}

func unsupported(c *httpx.Context) error {
	if _, _, err := check(c, c.Operation()); err != nil {
		return err
	}
	return errs.New(errs.ReasonCapabilityUnsupported, "not in this version of mdm/partner (supplier and employee "+
		"roles, merging, bank accounts come later)")
}

// Services registers the system surface: every component of the deployment resolves partners.
func (m *Module) Services(s *grpcx.Services) error {
	s.Register(&partnerv3.SourceService_ServiceDesc, m, map[string]grpcx.Method{
		"Snapshot": {Operation: OpSystem, Callers: []string{grpcx.AnyComponent}},
		"Read":     {Operation: OpSystem, Callers: []string{grpcx.AnyComponent}}})
	s.Register(&partnerv3.PartnerService_ServiceDesc, m, map[string]grpcx.Method{
		"CheckEligibility": {Operation: OpSystem, Callers: []string{grpcx.AnyComponent}},
		"Resolve":          {Operation: OpSystem, Callers: []string{grpcx.AnyComponent}}})
	return nil
}

// check admits the request's subject to op and returns the context to work in and the acting principal.
func check(c *httpx.Context, op string) (context.Context, string, error) {
	s, _ := sdkauthz.From(c.Ctx())
	ctx, _, err := s.Check(c.Ctx(), op, sdkauthz.Facts{})
	if err != nil {
		return ctx, "", err
	}
	u, _ := identity.UserFrom(ctx)
	return ctx, u.Subject, nil
}

func invalid(field, why string) error {
	return errs.New(errs.ReasonRequestInvalid, why).WithViolations(errs.Violation{Field: field, Reason: "INVALID", Description: why})
}

func notFound(what string) error { return errs.New(errs.ReasonNotFound, what+" not found") }

// change records the business change with the record's new state (protocol 03), in the transaction that made it.
func (m *Module) change(ctx context.Context, tx db.Tx, actor, op, typ, id string, after any) error {
	_, err := m.rt.Audit.Write(ctx, tx, audit.Record{Category: audit.BusinessChange, Operation: op, Outcome: audit.Succeeded,
		Actor: actor, Target: map[string]any{"type": typ, "id": id}, Changes: map[string]any{"after": after}})
	return err
}

func same(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func searchOf(p Partner) string {
	vals := []string{p.Name}
	for _, n := range p.Names {
		vals = append(vals, n)
	}
	if p.TaxID != nil {
		vals = append(vals, *p.TaxID)
	}
	return search.Text(vals...)
}

type partnerIn struct {
	Version   *int64             `json:"version"`
	Kind      string             `json:"kind"`
	Name      *string            `json:"name"`
	Names     *map[string]string `json:"names"`
	Country   string             `json:"country"`
	TaxID     json.RawMessage    `json:"tax_id"` // absent: unchanged; null: none
	Contacts  *[]Contact         `json:"contacts"`
	Addresses *[]Address         `json:"addresses"`
}

// apply checks what is given and puts it into p; contacts and addresses without an ID get one.
func (in partnerIn) apply(p *Partner) error {
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return invalid("name", "name cannot be empty")
		}
		p.Name = *in.Name
	}
	if in.Names != nil {
		for tag, n := range *in.Names {
			if tag == "" || len(tag) > 35 || strings.TrimSpace(n) == "" {
				return invalid("names", "names maps a language tag to a non-empty name")
			}
		}
		p.Names = *in.Names
	}
	if len(in.TaxID) > 0 {
		if string(in.TaxID) == "null" {
			p.TaxID = nil
		} else {
			var s string
			if json.Unmarshal(in.TaxID, &s) != nil || strings.TrimSpace(s) == "" || len(s) > 64 {
				return invalid("tax_id", "tax_id is a non-empty string of at most 64, or null")
			}
			s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
			p.TaxID = &s
		}
	}
	if in.Contacts != nil {
		for i, c := range *in.Contacts {
			if strings.TrimSpace(c.Name) == "" {
				return invalid(fmt.Sprintf("contacts[%d].name", i), "a contact needs a name")
			}
			if c.ID == "" {
				(*in.Contacts)[i].ID = ids.New().String()
			} else if !uuidForm.MatchString(c.ID) {
				return invalid(fmt.Sprintf("contacts[%d].id", i), "a contact's id is a UUID")
			}
		}
		p.Contacts = *in.Contacts
	}
	if in.Addresses != nil {
		for i, a := range *in.Addresses {
			field := fmt.Sprintf("addresses[%d]", i)
			switch {
			case a.Kind != "registered" && a.Kind != "billing" && a.Kind != "shipping":
				return invalid(field+".kind", "kind is registered, billing or shipping")
			case len(a.Lines) == 0 || len(a.Lines) > 4 || strings.TrimSpace(a.City) == "" || !country.MatchString(a.Country):
				return invalid(field, "an address has 1-4 lines, a city and an ISO 3166-1 country")
			case a.ID != "" && !uuidForm.MatchString(a.ID):
				return invalid(field+".id", "an address's id is a UUID")
			}
			if a.ID == "" {
				(*in.Addresses)[i].ID = ids.New().String()
			}
		}
		p.Addresses = *in.Addresses
	}
	return nil
}

type answer struct {
	Partner    Partner  `json:"partner"`
	Duplicates []string `json:"duplicate_candidates"`
}

// createPartner creates a partner once per idempotency key (P4.21-P4.25): a partner has no natural key (the tax ID
// may be absent or shared), so the key is what makes a retried creation safe.
func (m *Module) createPartner(c *httpx.Context) error {
	ctx, actor, err := check(c, OpCreate)
	if err != nil {
		return err
	}
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in partnerIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Kind != "organization" && in.Kind != "person" {
		return invalid("kind", "kind is organization or person")
	}
	if !country.MatchString(in.Country) {
		return invalid("country", "country is an ISO 3166-1 alpha-2 code")
	}
	if in.Name == nil {
		return invalid("name", "name is required")
	}
	p := Partner{ID: ids.New().String(), Kind: in.Kind, Country: in.Country, State: "active", Names: map[string]string{},
		Contacts: []Contact{}, Addresses: []Address{}}
	if err := in.apply(&p); err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.rt.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		names, _ := json.Marshal(p.Names)
		contacts, _ := json.Marshal(p.Contacts)
		addresses, _ := json.Marshal(p.Addresses)
		if _, err := tx.Exec(ctx, `INSERT INTO partner (id, kind, name, names, country, tax_id, contacts, addresses, search,
			search_version) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, p.ID, p.Kind, p.Name, names, p.Country,
			p.TaxID, contacts, addresses, searchOf(p), search.Version); err != nil {
			return idem.Outcome{}, err
		}
		created, err := getPartner(ctx, tx, p.ID, false)
		if err != nil {
			return idem.Outcome{}, err
		}
		if err := m.emit(ctx, tx, PartnerChanged, created.ID, created.Version, created); err != nil {
			return idem.Outcome{}, err
		}
		if err := m.change(ctx, tx, actor, OpCreate, Partners, created.ID, created); err != nil {
			return idem.Outcome{}, err
		}
		res, _ := json.Marshal(map[string]string{"id": created.ID})
		return idem.Outcome{Status: 200, Result: res}, nil
	})
	if err != nil {
		return err
	}
	var res struct{ ID string }
	_ = json.Unmarshal(out.Result, &res)
	return m.answerWith(c, ctx, res.ID) // a replay answers with the partner as it is now
}

func (m *Module) answerWith(c *httpx.Context, ctx context.Context, id string) error {
	var a answer
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		if a.Partner, err = getPartner(ctx, tx, id, false); err != nil {
			return err
		}
		a.Duplicates, err = duplicates(ctx, tx, a.Partner)
		return err
	})
	if errors.Is(err, errNone) {
		return notFound("partner")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, a)
}

func (m *Module) partner(ctx context.Context, id string) (Partner, error) {
	if !uuidForm.MatchString(id) {
		return Partner{}, notFound("partner")
	}
	var p Partner
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		p, err = getPartner(ctx, tx, id, false)
		return err
	})
	if errors.Is(err, errNone) {
		return p, notFound("partner")
	}
	return p, err
}

func (m *Module) getPartner(c *httpx.Context) error {
	ctx, _, err := check(c, OpRead)
	if err != nil {
		return err
	}
	p, err := m.partner(ctx, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(200, p)
}

// changePartner locks the partner, applies the change and writes, publishes and audits it if it changed.
func (m *Module) changePartner(c *httpx.Context, ctx context.Context, actor string, version *int64,
	apply func(*Partner) error) (Partner, error) {
	id := c.Param("id")
	if !uuidForm.MatchString(id) {
		return Partner{}, notFound("partner")
	}
	var out Partner
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		cur, err := getPartner(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if version != nil && *version != cur.Version {
			return errVersion.New(fmt.Sprintf("the partner is at version %d", cur.Version))
		}
		next := cur
		if err := apply(&next); err != nil {
			return err
		}
		if same(cur, next) {
			out = cur
			return nil
		}
		names, _ := json.Marshal(next.Names)
		contacts, _ := json.Marshal(next.Contacts)
		addresses, _ := json.Marshal(next.Addresses)
		if _, err := tx.Exec(ctx, `UPDATE partner SET name = $2, names = $3, tax_id = $4, state = $5, contacts = $6,
			addresses = $7, search = $8, search_version = $9, version = version + 1, updated_at = now() WHERE id = $1`,
			id, next.Name, names, next.TaxID, next.State, contacts, addresses, searchOf(next), search.Version); err != nil {
			return err
		}
		if out, err = getPartner(ctx, tx, id, false); err != nil {
			return err
		}
		if err := m.emit(ctx, tx, PartnerChanged, out.ID, out.Version, out); err != nil {
			return err
		}
		return m.change(ctx, tx, actor, OpEdit, Partners, out.ID, out)
	})
	if errors.Is(err, errNone) {
		return out, notFound("partner")
	}
	return out, err
}

func (m *Module) updatePartner(c *httpx.Context) error {
	ctx, actor, err := check(c, OpEdit)
	if err != nil {
		return err
	}
	var in partnerIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Kind != "" || in.Country != "" {
		return invalid("kind", "kind and country do not change")
	}
	p, err := m.changePartner(c, ctx, actor, in.Version, func(p *Partner) error {
		if p.State == "retired" {
			return errState.New("a retired partner does not change")
		}
		return in.apply(p)
	})
	if err != nil {
		return err
	}
	return m.answerWith(c, ctx, p.ID)
}

// partnerState moves a partner through protocol 07's lifecycle: active and suspended both ways, either to retired,
// never back from retired. Its roles keep their own states; eligibility needs both active.
func (m *Module) partnerState(c *httpx.Context) error {
	ctx, actor, err := check(c, OpEdit)
	if err != nil {
		return err
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
	p, err := m.changePartner(c, ctx, actor, in.Version, func(p *Partner) error {
		if p.State == "retired" && in.State != "retired" {
			return errState.New("a retired partner stays retired")
		}
		p.State = in.State
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(200, p)
}

func checkCurrency(v *string) error {
	if v != nil && !isoCcy.MatchString(*v) {
		return invalid("default_currency", "default_currency is an ISO 4217 code")
	}
	return nil
}
