package partner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/events"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	"github.com/jackc/pgx/v5"
)

// aggregate is how the source methods read one kind of record.
type aggregate struct {
	table, cols string
	data        func(pgx.Row) (string, int64, any, error) // the ID, the version and the payload
}

var aggregates = map[partnerv3.Aggregate]aggregate{
	partnerv3.Aggregate_AGGREGATE_PARTNER: {"partner", partnerCols, func(r pgx.Row) (string, int64, any, error) {
		v, err := scanPartner(r)
		return v.ID, v.Version, v, err
	}},
	partnerv3.Aggregate_AGGREGATE_PARTNER_ROLE: {"partner_role", roleCols, func(r pgx.Row) (string, int64, any, error) {
		v, err := scanRole(r)
		return v.ID, v.Version, v, err
	}},
}

func (m *Module) stamp(ctx context.Context, tx db.Tx) (*partnerv3.Stamp, error) {
	st, err := events.ReadStamp(ctx, tx, m.instance, events.Stream{Name: Stream, Partitions: 4})
	if err != nil {
		return nil, err
	}
	return &partnerv3.Stamp{Deployment: m.deployment, Instance: st.Instance, Epoch: st.Epoch.String(), Positions: st.Positions}, nil
}

// readByID reads the records of one aggregate by ID (only well-formed IDs reach the database) and returns the found
// ones by ID.
func readByID(ctx context.Context, tx db.Tx, a aggregate, idList []string) (map[string]*partnerv3.Record, error) {
	var valid []string
	for _, id := range idList {
		if uuidForm.MatchString(id) {
			valid = append(valid, id)
		}
	}
	found := map[string]*partnerv3.Record{}
	if len(valid) == 0 {
		return found, nil
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = ANY($1::uuid[])`, a.cols, a.table), valid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		id, version, data, err := a.data(rows)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		found[id] = &partnerv3.Record{Id: id, Version: version, Data: raw}
	}
	return found, rows.Err()
}

// Snapshot pages through one aggregate by ID, with the stamp of the same snapshot (protocol 02, S3).
func (m *Module) Snapshot(ctx context.Context, in *partnerv3.SnapshotRequest) (*partnerv3.SnapshotResponse, error) {
	a, ok := aggregates[in.GetAggregate()]
	if !ok {
		return nil, invalid("aggregate", "an aggregate of this component")
	}
	limit := int(in.GetLimit())
	if limit == 0 {
		limit = MaxBatch
	}
	if limit < 0 || limit > MaxBatch {
		return nil, errs.New(errs.ReasonBatchTooLarge, fmt.Sprintf("at most %d records a page", MaxBatch))
	}
	after := in.GetCursor()
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	} else if !uuidForm.MatchString(after) {
		return nil, errs.New(errs.ReasonCursorInvalid, "the cursor cannot be read")
	}
	out := &partnerv3.SnapshotResponse{}
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		if out.Stamp, err = m.stamp(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id > $1::uuid ORDER BY id LIMIT %d`,
			a.cols, a.table, limit+1), after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Records) == limit {
				out.NextCursor = out.Records[len(out.Records)-1].Id
				break
			}
			id, version, data, err := a.data(rows)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(data)
			if err != nil {
				return err
			}
			out.Records = append(out.Records, &partnerv3.Record{Id: id, Version: version, Data: raw})
		}
		return rows.Err()
	}, db.Snapshot())
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Read answers by ID in the requested order: exists, or purged (nothing is deleted in this version).
func (m *Module) Read(ctx context.Context, in *partnerv3.ReadRequest) (*partnerv3.ReadResponse, error) {
	a, ok := aggregates[in.GetAggregate()]
	if !ok {
		return nil, invalid("aggregate", "an aggregate of this component")
	}
	if len(in.GetIds()) > MaxBatch {
		return nil, errs.New(errs.ReasonBatchTooLarge, fmt.Sprintf("at most %d IDs a call", MaxBatch))
	}
	out := &partnerv3.ReadResponse{}
	var found map[string]*partnerv3.Record
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		if out.Stamp, err = m.stamp(ctx, tx); err != nil {
			return err
		}
		found, err = readByID(ctx, tx, a, in.GetIds())
		return err
	}, db.Snapshot())
	if err != nil {
		return nil, err
	}
	for _, id := range in.GetIds() {
		r := &partnerv3.ReadResult{Id: id, State: partnerv3.ReadState_READ_STATE_PURGED}
		if f, ok := found[id]; ok {
			r = &partnerv3.ReadResult{Id: id, State: partnerv3.ReadState_READ_STATE_EXISTS, Version: f.Version, Data: f.Data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

var purposes = map[partnerv3.Purpose]string{partnerv3.Purpose_PURPOSE_SELL: "sell",
	partnerv3.Purpose_PURPOSE_PURCHASE: "purchase", partnerv3.Purpose_PURPOSE_PAY_EMPLOYEE: "pay_employee"}

// CheckEligibility is PartnerService.CheckEligibility (the same answer as GET /api/partner/eligibility).
func (m *Module) CheckEligibility(ctx context.Context, in *partnerv3.CheckEligibilityRequest) (*partnerv3.CheckEligibilityResponse, error) {
	e, err := m.eligibility(ctx, in.GetPartnerId(), in.GetLegalEntityId(), purposes[in.GetPurpose()])
	if err != nil {
		return nil, err
	}
	return &partnerv3.CheckEligibilityResponse{Eligible: e.Eligible, Reason: e.Reason, PartnerVersion: e.PartnerVersion,
		RoleId: e.RoleID, RoleVersion: e.RoleVersion, SurvivorId: e.SurvivorID, EvaluatedAt: e.EvaluatedAt}, nil
}

// Resolve answers protocol 07's resolution by ID: current, or not found (no merges or erasure in this version, so no
// aliases or tombstones yet).
func (m *Module) Resolve(ctx context.Context, in *partnerv3.ResolveRequest) (*partnerv3.ResolveResponse, error) {
	if len(in.GetIds()) > MaxBatch {
		return nil, errs.New(errs.ReasonBatchTooLarge, fmt.Sprintf("at most %d IDs a call", MaxBatch))
	}
	var found map[string]*partnerv3.Record
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		found, err = readByID(ctx, tx, aggregates[partnerv3.Aggregate_AGGREGATE_PARTNER], in.GetIds())
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &partnerv3.ResolveResponse{}
	for _, id := range in.GetIds() {
		r := &partnerv3.Resolved{Id: id, Resolution: partnerv3.Resolution_RESOLUTION_NOT_FOUND}
		if f, ok := found[id]; ok {
			r = &partnerv3.Resolved{Id: id, Resolution: partnerv3.Resolution_RESOLUTION_CURRENT, Version: f.Version, Data: f.Data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}
