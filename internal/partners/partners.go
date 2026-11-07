package partners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	core "github.com/brickKit/mdm-partner/v3/internal/core"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/search"
)

func searchOf(p core.Partner) string {
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
	Contacts  *[]core.Contact    `json:"contacts"`
	Addresses *[]core.Address    `json:"addresses"`
}

// apply checks what is given and puts it into p; contacts and addresses without an ID get one.
func (in partnerIn) apply(p *core.Partner) error {
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return core.Invalid("name", "name cannot be empty")
		}
		p.Name = *in.Name
	}
	if in.Names != nil {
		for tag, n := range *in.Names {
			if tag == "" || len(tag) > 35 || strings.TrimSpace(n) == "" {
				return core.Invalid("names", "names maps a language tag to a non-empty name")
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
				return core.Invalid("tax_id", "tax_id is a non-empty string of at most 64, or null")
			}
			s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
			p.TaxID = &s
		}
	}
	if in.Contacts != nil {
		for i, c := range *in.Contacts {
			if strings.TrimSpace(c.Name) == "" {
				return core.Invalid(fmt.Sprintf("contacts[%d].name", i), "a contact needs a name")
			}
			if c.ID == "" {
				(*in.Contacts)[i].ID = ids.New().String()
			} else if !core.UUIDForm.MatchString(c.ID) {
				return core.Invalid(fmt.Sprintf("contacts[%d].id", i), "a contact's id is a UUID")
			}
		}
		p.Contacts = *in.Contacts
	}
	if in.Addresses != nil {
		for i, a := range *in.Addresses {
			field := fmt.Sprintf("addresses[%d]", i)
			switch {
			case a.Kind != "registered" && a.Kind != "billing" && a.Kind != "shipping":
				return core.Invalid(field+".kind", "kind is registered, billing or shipping")
			case len(a.Lines) == 0 || len(a.Lines) > 4 || strings.TrimSpace(a.City) == "" || !core.Country.MatchString(a.Country):
				return core.Invalid(field, "an address has 1-4 lines, a city and an ISO 3166-1 country")
			case a.ID != "" && !core.UUIDForm.MatchString(a.ID):
				return core.Invalid(field+".id", "an address's id is a UUID")
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
	Partner    core.Partner `json:"partner"`
	Duplicates []string     `json:"duplicate_candidates"`
}

// CreatePartner creates a partner once per idempotency key (P4.21-P4.25): a partner has no natural key (the tax ID
// may be absent or shared), so the key is what makes a retried creation safe.
func CreatePartner(m *core.Base, c *httpx.Context) error {
	ctx, actor, err := core.Check(c, core.OpCreate)
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
		return core.Invalid("kind", "kind is organization or person")
	}
	if !core.Country.MatchString(in.Country) {
		return core.Invalid("country", "country is an ISO 3166-1 alpha-2 code")
	}
	if in.Name == nil {
		return core.Invalid("name", "name is required")
	}
	p := core.Partner{ID: ids.New().String(), Kind: in.Kind, Country: in.Country, State: "active", Names: map[string]string{},
		Contacts: []core.Contact{}, Addresses: []core.Address{}}
	if err := in.apply(&p); err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		names, _ := json.Marshal(p.Names)
		contacts, _ := json.Marshal(p.Contacts)
		addresses, _ := json.Marshal(p.Addresses)
		if _, err := tx.Exec(ctx, `INSERT INTO partner (id, kind, name, names, country, tax_id, contacts, addresses, search,
			search_version) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, p.ID, p.Kind, p.Name, names, p.Country,
			p.TaxID, contacts, addresses, searchOf(p), search.Version); err != nil {
			return idem.Outcome{}, err
		}
		created, err := core.GetPartner(ctx, tx, p.ID, false)
		if err != nil {
			return idem.Outcome{}, err
		}
		if err := core.Emit(m, ctx, tx, core.PartnerChanged, created.ID, created.Version, created); err != nil {
			return idem.Outcome{}, err
		}
		if err := core.Change(m, ctx, tx, actor, core.OpCreate, core.Partners, created.ID, created); err != nil {
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
	return answerWith(m, c, ctx, res.ID) // a replay answers with the partner as it is now
}

func answerWith(m *core.Base, c *httpx.Context, ctx context.Context, id string) error {
	var a answer
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		if a.Partner, err = core.GetPartner(ctx, tx, id, false); err != nil {
			return err
		}
		a.Duplicates, err = core.Duplicates(ctx, tx, a.Partner)
		return err
	})
	if errors.Is(err, core.ErrNone) {
		return core.NotFound("partner")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, a)
}

func Partner(m *core.Base, ctx context.Context, id string) (core.Partner, error) {
	if !core.UUIDForm.MatchString(id) {
		return core.Partner{}, core.NotFound("partner")
	}
	var p core.Partner
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		p, err = core.GetPartner(ctx, tx, id, false)
		return err
	})
	if errors.Is(err, core.ErrNone) {
		return p, core.NotFound("partner")
	}
	return p, err
}

func GetPartner(m *core.Base, c *httpx.Context) error {
	ctx, _, err := core.Check(c, core.OpRead)
	if err != nil {
		return err
	}
	p, err := Partner(m, ctx, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(200, p)
}

// changePartner locks the partner, applies the change and writes, publishes and audits it if it changed.
func changePartner(m *core.Base, c *httpx.Context, ctx context.Context, actor string, version *int64,
	apply func(*core.Partner) error) (core.Partner, error) {
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.Partner{}, core.NotFound("partner")
	}
	var out core.Partner
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		cur, err := core.GetPartner(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if version != nil && *version != cur.Version {
			return core.ErrVersion.New(fmt.Sprintf("the partner is at version %d", cur.Version))
		}
		next := cur
		if err := apply(&next); err != nil {
			return err
		}
		if core.Same(cur, next) {
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
		if out, err = core.GetPartner(ctx, tx, id, false); err != nil {
			return err
		}
		if err := core.Emit(m, ctx, tx, core.PartnerChanged, out.ID, out.Version, out); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpEdit, core.Partners, out.ID, out)
	})
	if errors.Is(err, core.ErrNone) {
		return out, core.NotFound("partner")
	}
	return out, err
}

func UpdatePartner(m *core.Base, c *httpx.Context) error {
	ctx, actor, err := core.Check(c, core.OpEdit)
	if err != nil {
		return err
	}
	var in partnerIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Kind != "" || in.Country != "" {
		return core.Invalid("kind", "kind and country do not change")
	}
	p, err := changePartner(m, c, ctx, actor, in.Version, func(p *core.Partner) error {
		if p.State == "retired" {
			return core.ErrState.New("a retired partner does not change")
		}
		return in.apply(p)
	})
	if err != nil {
		return err
	}
	return answerWith(m, c, ctx, p.ID)
}

// PartnerState moves a partner through protocol 07's lifecycle: active and suspended both ways, either to retired,
// never back from retired. Its roles keep their own states; eligibility needs both active.
func PartnerState(m *core.Base, c *httpx.Context) error {
	ctx, actor, err := core.Check(c, core.OpEdit)
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
		return core.Invalid("state", "state is active, suspended or retired")
	}
	p, err := changePartner(m, c, ctx, actor, in.Version, func(p *core.Partner) error {
		if p.State == "retired" && in.State != "retired" {
			return core.ErrState.New("a retired partner stays retired")
		}
		p.State = in.State
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(200, p)
}

func CheckCurrency(v *string) error {
	if v != nil && !core.IsoCcy.MatchString(*v) {
		return core.Invalid("default_currency", "default_currency is an ISO 4217 code")
	}
	return nil
}

// partnerList is the list of the user surface (protocol 14: cursor pages ordered by name then ID, the filters and
// keyword declared here). Every field is ordinary, so a row is listed whole or not at all.
var partnerList = list.Spec{Operation: core.OpRead, Alias: "p", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "name", Column: "name", Kind: list.Text, Sortable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true},
		{Field: "kind", Column: "kind", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "names", Column: "search", Fields: []string{"name", "names", "tax_id"}}}}

func ListPartners(m *core.Base, c *httpx.Context) error {
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	var f []list.Filter
	for _, k := range []string{"state", "kind"} {
		if v := c.Query(k); v != "" {
			f = append(f, list.Filter{Field: k, Op: "eq", Values: []string{v}})
		}
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, partnerList, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "name"}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "partner", core.PartnerCols, core.ScanPartner,
		func(p core.Partner) ([]string, string) { return []string{p.Name}, p.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"partners": out, "next_cursor": next})
}
