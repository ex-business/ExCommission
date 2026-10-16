package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Repository struct{}

func active(status string) any {
	if status == "DRAFT" || status == "QUEUED" || status == "BLOCKED" {
		return 1
	}
	return nil
}
func (Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, i model.Instruction) error {
	body, _ := json.Marshal(i)
	_, err := tx.ExecContext(ctx, `INSERT INTO payout_instructions VALUES(?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, i.ID, i.StatementID, i.Status, i.Revision, body, active(i.Status), i.Created)
	return err
}
func (Repository) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string, lock bool) (model.Instruction, error) {
	var i model.Instruction
	var body []byte
	q := `SELECT body FROM payout_instructions WHERE tenant_id=? AND app_id=? AND instruction_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return i, errors.New("NOT_FOUND")
	}
	if err != nil {
		return i, err
	}
	err = json.Unmarshal(body, &i)
	return i, err
}
func (Repository) Update(ctx context.Context, tx db.Executor, s foundation.Scope, i model.Instruction, previous uint64) error {
	body, _ := json.Marshal(i)
	result, err := tx.ExecContext(ctx, `UPDATE payout_instructions SET status=?,revision=?,body=?,active_slot=? WHERE tenant_id=? AND app_id=? AND instruction_id=? AND revision=?`, i.Status, i.Revision, body, active(i.Status), s.Tenant, s.App, i.ID, previous)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("REVISION_CONFLICT")
	}
	return err
}
func (r Repository) Queued(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.Instruction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT instruction_id FROM payout_instructions WHERE tenant_id=? AND app_id=? AND status='QUEUED' ORDER BY created_at,instruction_id LIMIT 20`, s.Tenant, s.App)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.Instruction{}
	for _, id := range ids {
		i, e := r.Get(ctx, tx, s, id, false)
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, nil
}
