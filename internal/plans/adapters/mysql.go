package adapters

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func Hash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func (Repository) Create(ctx context.Context, tx ports.Executor, s foundation.Scope, c domain.Config, body []byte, hash string, at time.Time) error {
	if !s.Valid() || c.Revision != 0 || hash != Hash(body) {
		return foundation.ErrValidation
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO plan_drafts VALUES(?,?,?,0,'DRAFT',?,?,?,?)`, s.Tenant, s.App, c.PlanID, body, hash, at.UTC(), at.UTC())
	return err
}
func (Repository) Read(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, lock bool) (domain.Draft, error) {
	statement := `SELECT config,revision,config_hash,status FROM plan_drafts WHERE tenant_id=? AND app_id=? AND plan_id=?`
	if lock {
		statement += " FOR UPDATE"
	}
	var draft domain.Draft
	var body []byte
	err := tx.QueryRowContext(ctx, statement, s.Tenant, s.App, id).Scan(&body, &draft.Revision, &draft.Hash, &draft.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return draft, errors.New("NOT_FOUND")
	}
	if err != nil {
		return draft, err
	}
	err = json.Unmarshal(body, &draft.Config)
	return draft, err
}
func (Repository) Update(ctx context.Context, tx ports.Executor, s foundation.Scope, c domain.Config, body []byte, hash string, expected uint64, at time.Time) error {
	if c.Revision != expected+1 || hash != Hash(body) {
		return foundation.ErrValidation
	}
	result, err := tx.ExecContext(ctx, `UPDATE plan_drafts SET config=?,config_hash=?,revision=revision+1,status='DRAFT',updated_at=? WHERE tenant_id=? AND app_id=? AND plan_id=? AND revision=? AND status IN ('DRAFT','PUBLISHED')`, body, hash, at.UTC(), s.Tenant, s.App, c.PlanID, expected)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("REVISION_CONFLICT")
	}
	return nil
}
func (Repository) SetPending(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, revision uint64, hash string) error {
	result, err := tx.ExecContext(ctx, `UPDATE plan_drafts SET status='PENDING_APPROVAL' WHERE tenant_id=? AND app_id=? AND plan_id=? AND revision=? AND config_hash=? AND status='DRAFT'`, s.Tenant, s.App, id, revision, hash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("STALE_APPROVAL")
	}
	return nil
}
func (r Repository) Publish(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, revision uint64, hash, version string, effective, executed time.Time) error {
	if effective.Before(executed) {
		return foundation.ErrValidation
	}
	// 同一应用路由空间加锁；同plan选版和跨plan路由冲突在同一事务检查。
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_route_spaces VALUES(?,?) ON DUPLICATE KEY UPDATE app_id=app_id`, s.Tenant, s.App); err != nil {
		return err
	}
	draft, err := r.Read(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if draft.Revision != revision || draft.Hash != hash || draft.Status != "PENDING_APPROVAL" {
		return errors.New("STALE_APPROVAL")
	}
	var conflict int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_versions WHERE tenant_id=? AND app_id=? AND product_code=? AND event_type=? AND scene=? AND plan_id<>? AND (effective_to IS NULL OR effective_to>?)`, s.Tenant, s.App, draft.Config.Route.Product, draft.Config.Route.EventType, draft.Config.Route.Scene, id, effective.UTC()).Scan(&conflict)
	if err != nil {
		return err
	}
	if conflict != 0 {
		return errors.New("STATE_CONFLICT")
	}
	var number uint64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_number),0) FROM plan_versions WHERE tenant_id=? AND app_id=? AND plan_id=?`, s.Tenant, s.App, id).Scan(&number)
	if err != nil {
		return err
	}
	var future int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_versions WHERE tenant_id=? AND app_id=? AND plan_id=? AND effective_from>=?`, s.Tenant, s.App, id, effective.UTC()).Scan(&future)
	if err != nil {
		return err
	}
	if future != 0 {
		return errors.New("STATE_CONFLICT")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plan_versions SET effective_to=? WHERE tenant_id=? AND app_id=? AND plan_id=? AND effective_to IS NULL`, effective.UTC(), s.Tenant, s.App, id); err != nil {
		return err
	}
	body, err := json.Marshal(draft.Config)
	if err != nil {
		return err
	}
	canonical, err := foundation.CanonicalJSON(body)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_versions VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, id, version, number+1, canonical, hash, draft.Config.Route.Product, draft.Config.Route.EventType, draft.Config.Route.Scene, effective.UTC(), nil, executed.UTC()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE plan_drafts SET status='PUBLISHED' WHERE tenant_id=? AND app_id=? AND plan_id=?`, s.Tenant, s.App, id)
	return err
}
func (Repository) Select(ctx context.Context, tx ports.Executor, s foundation.Scope, route domain.Route, at time.Time) (domain.Version, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version_id,version_number,config,config_hash,effective_from,effective_to FROM plan_versions WHERE tenant_id=? AND app_id=? AND product_code=? AND event_type=? AND scene=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?)`, s.Tenant, s.App, route.Product, route.EventType, route.Scene, at.UTC(), at.UTC())
	if err != nil {
		return domain.Version{}, err
	}
	defer rows.Close()
	var selected domain.Version
	count := 0
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&selected.ID, &selected.Number, &body, &selected.Hash, &selected.From, &selected.To); err != nil {
			return selected, err
		}
		if err = json.Unmarshal(body, &selected.Config); err != nil {
			return selected, err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return selected, err
	}
	if count == 0 {
		return selected, errors.New("NO_EFFECTIVE_PLAN_VERSION")
	}
	if count != 1 {
		return selected, errors.New("STATE_CONFLICT")
	}
	return selected, nil
}
func (Repository) Versions(ctx context.Context, tx ports.Executor, s foundation.Scope, id string) ([]domain.Version, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version_id,version_number,config,config_hash,effective_from,effective_to FROM plan_versions WHERE tenant_id=? AND app_id=? AND plan_id=? ORDER BY version_number`, s.Tenant, s.App, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []domain.Version{}
	for rows.Next() {
		var version domain.Version
		var body []byte
		if err = rows.Scan(&version.ID, &version.Number, &body, &version.Hash, &version.From, &version.To); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &version.Config); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (r Repository) Reject(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, revision uint64, hash string, at time.Time) error {
	draft, err := r.Read(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if draft.Status != "PENDING_APPROVAL" || draft.Revision != revision || draft.Hash != hash {
		return errors.New("STALE_APPROVAL")
	}
	draft.Config.Revision++
	body, err := json.Marshal(draft.Config)
	if err != nil {
		return err
	}
	canonical, err := foundation.CanonicalJSON(body)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE plan_drafts SET revision=revision+1,status='DRAFT',config=?,config_hash=?,updated_at=? WHERE tenant_id=? AND app_id=? AND plan_id=?`, canonical, Hash(canonical), at.UTC(), s.Tenant, s.App, id)
	return err
}

func (Repository) AtPlan(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, at time.Time) (domain.Version, error) {
	var version domain.Version
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT version_id,version_number,config,config_hash,effective_from,effective_to FROM plan_versions WHERE tenant_id=? AND app_id=? AND plan_id=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?)`, s.Tenant, s.App, id, at.UTC(), at.UTC()).Scan(&version.ID, &version.Number, &body, &version.Hash, &version.From, &version.To)
	if errors.Is(err, sql.ErrNoRows) {
		return version, errors.New("NO_EFFECTIVE_PLAN_VERSION")
	}
	if err != nil {
		return version, err
	}
	err = json.Unmarshal(body, &version.Config)
	return version, err
}

func (r Repository) VersionPage(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, offset, limit int) ([]domain.Version, bool, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, false, foundation.ErrValidation
	}
	if _, err := r.Read(ctx, tx, s, id, false); err != nil {
		return nil, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT version_id,version_number,config,config_hash,effective_from,effective_to FROM plan_versions WHERE tenant_id=? AND app_id=? AND plan_id=? ORDER BY version_number LIMIT ? OFFSET ?`, s.Tenant, s.App, id, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	versions := []domain.Version{}
	for rows.Next() {
		var version domain.Version
		var body []byte
		if err = rows.Scan(&version.ID, &version.Number, &body, &version.Hash, &version.From, &version.To); err != nil {
			return nil, false, err
		}
		if err = json.Unmarshal(body, &version.Config); err != nil {
			return nil, false, err
		}
		versions = append(versions, version)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(versions) > limit
	if more {
		versions = versions[:limit]
	}
	return versions, more, nil
}

func (Repository) UnlockFailed(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, revision uint64, hash string) error {
	result, err := tx.ExecContext(ctx, `UPDATE plan_drafts SET status='DRAFT' WHERE tenant_id=? AND app_id=? AND plan_id=? AND revision=? AND config_hash=? AND status='PENDING_APPROVAL'`, s.Tenant, s.App, id, revision, hash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("STALE_APPROVAL")
	}
	return nil
}
