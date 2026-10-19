package partner

import (
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/list"
)

// partnerList is the list of the user surface (protocol 14: cursor pages ordered by name then ID, the filters and
// keyword declared here). Every field is ordinary, so a row is listed whole or not at all.
var partnerList = list.Spec{Operation: OpRead, Alias: "p", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "name", Column: "name", Kind: list.Text, Sortable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true},
		{Field: "kind", Column: "kind", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "names", Column: "search", Fields: []string{"name", "names", "tax_id"}}}}

func (m *Module) listPartners(c *httpx.Context) error {
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
	out, next, err := list.Page(ctx, m.rt.DB, q, "partner", partnerCols, scanPartner,
		func(p Partner) ([]string, string) { return []string{p.Name}, p.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"partners": out, "next_cursor": next})
}
