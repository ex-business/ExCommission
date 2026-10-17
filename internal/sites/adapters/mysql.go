package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/sites/domain"
	"time"
)

type Repository struct{}

func (Repository) Ensure(ctx context.Context, tx db.Executor, s foundation.Scope) error {
	_, err := tx.ExecContext(ctx, `INSERT IGNORE INTO site_agent_states VALUES(?,?,0,NULL)`, s.Tenant, s.App)
	return err
}
func (r Repository) Current(ctx context.Context, tx db.Executor, s foundation.Scope, lock bool) (model.Config, error) {
	q := `SELECT body FROM site_agent_states WHERE tenant_id=? AND app_id=?`
	if lock {
		q += " FOR SHARE"
	}
	var b []byte
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		if lock {
			if err = r.Ensure(ctx, tx, s); err != nil {
				return model.Config{}, err
			}
			return r.Current(ctx, tx, s, true)
		}
		return model.Config{Enabled: true, Venues: []string{}}, nil
	}
	if err != nil {
		return model.Config{}, err
	}
	if len(b) == 0 {
		return model.Config{Enabled: true, Venues: []string{}}, nil
	}
	var c model.Config
	err = json.Unmarshal(b, &c)
	return c, err
}
func (Repository) At(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time) (model.Config, error) {
	var b []byte
	err := tx.QueryRowContext(ctx, `SELECT body FROM site_agent_versions WHERE tenant_id=? AND app_id=? AND effective_from<=? ORDER BY revision DESC LIMIT 1`, s.Tenant, s.App, at).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Config{Enabled: true, Venues: []string{}}, nil
	}
	if err != nil {
		return model.Config{}, err
	}
	var c model.Config
	err = json.Unmarshal(b, &c)
	return c, err
}
func (r Repository) Apply(ctx context.Context, tx db.Executor, s foundation.Scope, in model.Change, approval string, at time.Time) error {
	if err := r.Ensure(ctx, tx, s); err != nil {
		return err
	}
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM site_agent_states WHERE tenant_id=? AND app_id=? FOR UPDATE`, s.Tenant, s.App).Scan(&revision); err != nil {
		return err
	}
	if revision != in.Revision {
		return errors.New("STALE_APPROVAL")
	}
	c := model.Config{Configured: true, Enabled: in.Enabled, PlanID: in.PlanID, Venues: in.Venues, Revision: revision + 1, Effective: &at}
	b, _ := json.Marshal(c)
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_agent_versions VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, c.Revision, at, b, approval); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE site_agent_states SET revision=?,body=? WHERE tenant_id=? AND app_id=?`, c.Revision, b, s.Tenant, s.App)
	return err
}
