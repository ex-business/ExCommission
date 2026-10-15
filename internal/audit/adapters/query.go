package adapters

import (
	"context"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Filter struct {
	Type, Actor, Object string
	From, To            time.Time
	Limit, Offset       int
}
type Record struct {
	ID        string            `json:"entry_id"`
	Subject   string            `json:"actor"`
	RequestID string            `json:"request_id"`
	Type      string            `json:"type"`
	Action    string            `json:"action"`
	Resource  string            `json:"object_ref"`
	Summary   map[string]string `json:"summary"`
	At        time.Time         `json:"occurred_at"`
}

func List(ctx context.Context, tx db.Executor, s foundation.Scope, f Filter) ([]Record, error) {
	if !s.Valid() || f.Limit < 1 || f.Limit > 100 || f.Offset < 0 || f.To.IsZero() || (!f.From.IsZero() && !f.From.Before(f.To)) {
		return nil, foundation.ErrValidation
	}
	if f.Type != "" && f.Type != "OPERATION" && f.Type != "SECURITY" {
		return nil, foundation.ErrValidation
	}
	q := `SELECT entry_id,subject_id,request_id,log_type,action,resource_ref,summary,occurred_at FROM audit_entries WHERE tenant_id=? AND app_id=? AND occurred_at<?`
	args := []any{s.Tenant, s.App, f.To}
	if !f.From.IsZero() {
		q += " AND occurred_at>=?"
		args = append(args, f.From)
	}
	for _, v := range []struct{ column, value string }{{"log_type", f.Type}, {"subject_id", f.Actor}, {"resource_ref", f.Object}} {
		if v.value != "" {
			q += " AND " + v.column + "=?"
			args = append(args, v.value)
		}
	}
	q += " ORDER BY occurred_at DESC,entry_id DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var r Record
		var raw []byte
		if err = rows.Scan(&r.ID, &r.Subject, &r.RequestID, &r.Type, &r.Action, &r.Resource, &raw, &r.At); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r.Summary); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func Handle(ctx context.Context, tx db.Executor, s foundation.Scope, id, record, subject, status, reason string, expected uint64, now time.Time) error {
	if !foundation.ValidID(reason) || (status != "OPEN" && status != "ACKNOWLEDGED" && status != "RESOLVED") {
		return foundation.ErrValidation
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE tenant_id=? AND app_id=? AND entry_id=? AND log_type='SECURITY'`, s.Tenant, s.App, id).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("NOT_FOUND")
	}
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO audit_security_states VALUES(?,?,?,'OPEN',0)`, s.Tenant, s.App, id); err != nil {
		return err
	}
	var revision uint64
	var previous string
	if err := tx.QueryRowContext(ctx, `SELECT revision,status FROM audit_security_states WHERE tenant_id=? AND app_id=? AND event_id=? FOR UPDATE`, s.Tenant, s.App, id).Scan(&revision, &previous); err != nil {
		return err
	}
	if revision != expected {
		return errors.New("REVISION_CONFLICT")
	}
	if previous == status {
		return errors.New("STATE_CONFLICT")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_handling_records VALUES(?,?,?,?,?,?,?,?)`, s.Tenant, s.App, record, id, subject, status, reason, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE audit_security_states SET status=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND event_id=?`, status, s.Tenant, s.App, id)
	return err
}
