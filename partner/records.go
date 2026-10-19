package partner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/jackc/pgx/v5"
)

// Contact is a formal contact of a partner.
type Contact struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// Address is a formal address of a partner.
type Address struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Lines      []string `json:"lines"`
	City       string   `json:"city"`
	Region     string   `json:"region,omitempty"`
	PostalCode string   `json:"postal_code,omitempty"`
	Country    string   `json:"country"`
}

// Partner is a partner's complete state: the payload of mdm.partner.changed.v1 and of its reads.
type Partner struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Names     map[string]string `json:"names"`
	Country   string            `json:"country"`
	TaxID     *string           `json:"tax_id"`
	State     string            `json:"state"`
	Contacts  []Contact         `json:"contacts"`
	Addresses []Address         `json:"addresses"`
	Version   int64             `json:"version"`
	UpdatedAt string            `json:"updated_at"`
}

// Role is a partner's role in a legal entity (mdm.partner_role.changed.v1).
type Role struct {
	ID              string  `json:"id"`
	PartnerID       string  `json:"partner_id"`
	Role            string  `json:"role"`
	LegalEntityID   string  `json:"legal_entity_id"`
	State           string  `json:"state"`
	DefaultCurrency *string `json:"default_currency"`
	PaymentTerms    *string `json:"payment_terms"`
	Version         int64   `json:"version"`
	UpdatedAt       string  `json:"updated_at"`
}

const (
	partnerCols = `id::text, kind, name, names, country, tax_id, state, contacts, addresses, version, updated_at`
	roleCols    = `id::text, partner_id::text, role, legal_entity_id::text, state, default_currency, payment_terms, version,
		updated_at`
)

func scanPartner(row pgx.Row) (Partner, error) {
	var p Partner
	var names, contacts, addresses []byte
	var at time.Time
	err := row.Scan(&p.ID, &p.Kind, &p.Name, &names, &p.Country, &p.TaxID, &p.State, &contacts, &addresses, &p.Version, &at)
	if err != nil {
		return p, err
	}
	p.UpdatedAt = canon.Time(at)
	return p, errors.Join(json.Unmarshal(names, &p.Names), json.Unmarshal(contacts, &p.Contacts),
		json.Unmarshal(addresses, &p.Addresses))
}

func scanRole(row pgx.Row) (Role, error) {
	var r Role
	var at time.Time
	err := row.Scan(&r.ID, &r.PartnerID, &r.Role, &r.LegalEntityID, &r.State, &r.DefaultCurrency, &r.PaymentTerms,
		&r.Version, &at)
	r.UpdatedAt = canon.Time(at)
	return r, err
}

// errNone is a lookup that found no row.
var errNone = errors.New("no such record")

func one[T any](row pgx.Row, scan func(pgx.Row) (T, error)) (T, error) {
	v, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNone
	}
	return v, err
}

func getPartner(ctx context.Context, tx db.Tx, id string, lock bool) (Partner, error) {
	q := `SELECT ` + partnerCols + ` FROM partner WHERE id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	return one(tx.QueryRow(ctx, q, id), scanPartner)
}

func getRole(ctx context.Context, tx db.Tx, partner, role, legalEntity string, lock bool) (Role, error) {
	q := `SELECT ` + roleCols + ` FROM partner_role WHERE partner_id = $1::uuid AND role = $2 AND legal_entity_id = $3::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	return one(tx.QueryRow(ctx, q, partner, role, legalEntity), scanRole)
}

// duplicates are the other partners with the same tax ID in the same country (D22: pointed out, merged by hand).
func duplicates(ctx context.Context, tx db.Tx, p Partner) ([]string, error) {
	out := []string{}
	if p.TaxID == nil {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM partner WHERE country = $1 AND tax_id = $2 AND id <> $3::uuid
		ORDER BY id`, p.Country, *p.TaxID, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// emit writes the record's full-state event into the outbox, in the transaction that changed it (protocol 02, S2).
func (m *Module) emit(ctx context.Context, tx db.Tx, typ, id string, version int64, data any) error {
	_, err := m.rt.Events.Emit(ctx, tx, Stream, events.Event{Type: typ, RecordID: id, Version: version,
		Projection: events.Full, Data: data})
	return err
}
