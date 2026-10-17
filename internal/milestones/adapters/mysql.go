package adapters

import (
	"context"
	"encoding/json"
	"github.com/ex-business/ExCommission/internal/milestones/domain"
	"github.com/ex-business/ExCommission/internal/milestones/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Program(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (ports.ProgramRecord, error) {
	var v ports.ProgramRecord
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT config,config_hash,approval_id FROM milestone_programs WHERE tenant_id=? AND app_id=? AND program_id=?`, s.Tenant, s.App, id).Scan(&body, &v.Hash, &v.ApprovalID)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(body, &v.Program)
	return v, err
}
func (Repository) Activate(ctx context.Context, tx db.Executor, s foundation.Scope, v ports.ProgramRecord, at time.Time) error {
	body, err := json.Marshal(v.Program)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO milestone_programs VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, v.Program.ID, body, v.Hash, v.ApprovalID, at)
	return err
}
func (Repository) LockMember(ctx context.Context, tx db.Executor, s foundation.Scope, program string, p domain.Progress, at time.Time) (domain.Progress, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO milestone_members VALUES(?,?,?,?,?,NULL,?) ON DUPLICATE KEY UPDATE user_id=user_id`, s.Tenant, s.App, program, p.UserID, body, at); err != nil {
		return p, err
	}
	err = tx.QueryRowContext(ctx, `SELECT progress FROM milestone_members WHERE tenant_id=? AND app_id=? AND program_id=? AND user_id=? FOR UPDATE`, s.Tenant, s.App, program, p.UserID).Scan(&body)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(body, &p)
	return p, err
}
func (Repository) Member(ctx context.Context, tx db.Executor, s foundation.Scope, program, user string) (domain.Progress, error) {
	var p domain.Progress
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT progress FROM milestone_members WHERE tenant_id=? AND app_id=? AND program_id=? AND user_id=?`, s.Tenant, s.App, program, user).Scan(&body)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(body, &p)
	return p, err
}
func (Repository) SaveMember(ctx context.Context, tx db.Executor, s foundation.Scope, program string, p domain.Progress, at time.Time) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE milestone_members SET progress=?,task_id=?,updated_at=? WHERE tenant_id=? AND app_id=? AND program_id=? AND user_id=?`, body, p.TaskID, at, s.Tenant, s.App, program, p.UserID)
	return err
}
func (Repository) Receipt(ctx context.Context, tx db.Executor, s foundation.Scope, source, event string) (string, foundation.CommandResult, error) {
	var hash string
	r := foundation.CommandResult{Status: 202}
	err := tx.QueryRowContext(ctx, `SELECT payload_hash,response_body,resource_id FROM milestone_observations WHERE tenant_id=? AND app_id=? AND source_id=? AND source_event_id=?`, s.Tenant, s.App, source, event).Scan(&hash, &r.Body, &r.ResourceID)
	r.Replayed = err == nil
	return hash, r, err
}
func (Repository) SaveReceipt(ctx context.Context, tx db.Executor, s foundation.Scope, o domain.Observation, body []byte, hash string, r foundation.CommandResult, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO milestone_observations VALUES(?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, o.Source, o.EventID, hash, body, r.Body, r.ResourceID, at)
	return err
}
