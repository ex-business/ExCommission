package adapters

import (
	"context"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Ensure(ctx context.Context, tx db.Executor, s foundation.Scope) error {
	if !s.Valid() {
		return foundation.ErrValidation
	}
	if _, e := tx.ExecContext(ctx, `INSERT IGNORE INTO affiliate_spaces VALUES(?,?,0)`, s.Tenant, s.App); e != nil {
		return e
	}
	// 场馆使用真实业务分类，图标采用同源分类素材；不把交易产品代码当作游戏类型。
	for _, v := range []model.Venue{{ID: "SPORTS", Name: "体育"}, {ID: "ESPORTS", Name: "电竞"}, {ID: "LIVE", Name: "真人"}, {ID: "LOTTERY", Name: "彩票"}, {ID: "POKER", Name: "棋牌"}, {ID: "SLOTS", Name: "电子"}, {ID: "FISHING", Name: "捕鱼"}} {
		icons := map[string]string{"SPORTS": "sports", "ESPORTS": "esports", "LIVE": "live", "LOTTERY": "lottery", "POKER": "poker", "SLOTS": "slots", "FISHING": "fishing"}
		v.IconURL = "/venue-icons/" + icons[v.ID] + ".svg"
		if _, e := tx.ExecContext(ctx, `INSERT IGNORE INTO affiliate_venue_types VALUES(?,?,?,?,?)`, s.Tenant, s.App, v.ID, v.Name, v.IconURL); e != nil {
			return e
		}
	}
	return nil
}
func (Repository) Configuration(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time, lock bool) (model.Configuration, error) {
	out := model.Configuration{}
	q := `SELECT revision FROM affiliate_spaces WHERE tenant_id=? AND app_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	if e := tx.QueryRowContext(ctx, q, s.Tenant, s.App).Scan(&out.Revision); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT revision,effective_from,body,approval_id FROM affiliate_config_versions WHERE tenant_id=? AND app_id=? ORDER BY revision DESC LIMIT 2`, s.Tenant, s.App)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v model.Version
		var b []byte
		if e = rows.Scan(&v.Revision, &v.Effective, &b, &v.ApprovalID); e != nil {
			return out, e
		}
		if e = json.Unmarshal(b, &v.Config); e != nil {
			return out, e
		}
		if v.Effective.After(at) {
			if out.Scheduled == nil {
				out.Scheduled = &v
			}
		} else if out.Current == nil {
			out.Current = &v
		}
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	// At most one future version is accepted, so two rows suffice for the public current/scheduled view.
	out.Configured = out.Current != nil
	return out, nil
}
func (Repository) Venues(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.Venue, error) {
	rows, e := tx.QueryContext(ctx, `SELECT venue_type_id,name,icon_url FROM affiliate_venue_types WHERE tenant_id=? AND app_id=? ORDER BY venue_type_id`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Venue{}
	for rows.Next() {
		var v model.Venue
		if e = rows.Scan(&v.ID, &v.Name, &v.IconURL); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r Repository) Apply(ctx context.Context, tx db.Executor, s foundation.Scope, in model.Change, approval string, at time.Time) error {
	if e := in.Normalize(); e != nil {
		return e
	}
	old, e := r.Configuration(ctx, tx, s, at, true)
	if e != nil {
		return e
	}
	if old.Revision != in.Revision {
		return errors.New("STALE_APPROVAL")
	}
	if old.Scheduled != nil {
		return errors.New("SCHEDULED_CONFIG_EXISTS")
	}
	effective := at
	if old.Current != nil {
		if e = in.Config.ValidateChange(old.Current.Config); e != nil {
			return e
		}
		_, effective = old.Current.Config.Day(at)
	}
	venues, e := r.Venues(ctx, tx, s)
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for _, v := range venues {
		known[v.ID] = true
	}
	for _, v := range in.Config.VenueIDs {
		if !known[v] {
			return errors.New("UNKNOWN_VENUE_TYPE")
		}
	}
	b, e := json.Marshal(in.Config)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO affiliate_config_versions VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, old.Revision+1, effective, b, approval); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE affiliate_spaces SET revision=revision+1 WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App)
	return e
}

// At reads the immutable profile active at a business instant, including arbitrarily old revisions.
func (Repository) At(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time) (model.Version, error) {
	var v model.Version
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT revision,effective_from,body,approval_id FROM affiliate_config_versions WHERE tenant_id=? AND app_id=? AND effective_from<=? ORDER BY effective_from DESC,revision DESC LIMIT 1`, s.Tenant, s.App, at).Scan(&v.Revision, &v.Effective, &b, &v.ApprovalID)
	if e == nil {
		e = json.Unmarshal(b, &v.Config)
	}
	return v, e
}

// Version 按不可变修订号读取业务依据，用于奖励记录回看，不用当前配置代替历史版本。
func (Repository) Version(ctx context.Context, tx db.Executor, s foundation.Scope, revision uint64) (model.Version, error) {
	var v model.Version
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT revision,effective_from,body,approval_id FROM affiliate_config_versions WHERE tenant_id=? AND app_id=? AND revision=?`, s.Tenant, s.App, revision).Scan(&v.Revision, &v.Effective, &b, &v.ApprovalID)
	if e == nil {
		e = json.Unmarshal(b, &v.Config)
	}
	return v, e
}
