package affiliateprograms

import (
	"context"
	"encoding/json"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"time"
)

type Repository struct{}

func (Repository) Versions(ctx context.Context, tx db.Executor, sc foundation.Scope) ([]Version, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_program_versions WHERE tenant_id=? AND app_id=? ORDER BY program_id,revision`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var raw []byte
		var v Version
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func Active(versions []Version, at time.Time) []Version {
	chosen := map[string]Version{}
	order := []string{}
	for _, v := range versions {
		if v.Effective.After(at) {
			continue
		}
		old, ok := chosen[v.ID]
		if !ok {
			order = append(order, v.ID)
		}
		if !ok || v.Revision > old.Revision {
			chosen[v.ID] = v
		}
	}
	out := []Version{}
	for _, id := range order {
		out = append(out, chosen[id])
	}
	return out
}
func (Repository) Save(ctx context.Context, tx db.Executor, sc foundation.Scope, v Version) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_program_versions VALUES(?,?,?,?,?,?)`, sc.Tenant, sc.App, v.ID, v.Revision, v.Effective, raw)
	return e
}
func (Repository) Facts(ctx context.Context, tx db.Executor, sc foundation.Scope, v Version, before time.Time) ([]affiliate.Fact, error) {
	rows, e := tx.QueryContext(ctx, `SELECT f.user_id,f.body,CAST(COALESCE(t.amount,0) AS CHAR),CAST(COALESCE(t.valid_bet,0) AS CHAR) FROM affiliate_program_facts f LEFT JOIN affiliate_reversal_totals t ON t.tenant_id=f.tenant_id AND t.app_id=f.app_id AND t.source_id=f.source_id AND t.kind=f.kind AND t.business_id=f.business_id WHERE f.tenant_id=? AND f.app_id=? AND f.program_id=? AND f.revision=? AND f.business_time<? ORDER BY f.business_time,f.source_id,f.business_id`, sc.Tenant, sc.App, v.ID, v.Revision, before.UTC())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []affiliate.Fact{}
	for rows.Next() {
		var user, ra, rb string
		var raw []byte
		var ev flow.Event
		if e = rows.Scan(&user, &raw, &ra, &rb); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &ev); e != nil {
			return nil, e
		}
		a, _ := new(big.Int).SetString(ev.Amount, 10)
		b, _ := new(big.Int).SetString(ev.ValidBet, 10)
		x, _ := new(big.Int).SetString(ra, 10)
		y, _ := new(big.Int).SetString(rb, 10)
		a.Sub(a, x)
		b.Sub(b, y)
		if a.Sign() < 0 || b.Sign() < 0 {
			return nil, foundation.ErrConflict
		}
		if a.Sign() == 0 {
			continue
		}
		ev.Amount = a.String()
		ev.ValidBet = b.String()
		out = append(out, ev.Fact(user))
	}
	return out, rows.Err()
}

type Receipt struct {
	Source     string     `json:"source_id"`
	EventID    string     `json:"event_id"`
	BusinessID string     `json:"business_id"`
	UserID     string     `json:"user_id"`
	At         time.Time  `json:"business_time"`
	Decisions  []Decision `json:"decisions"`
	Received   time.Time  `json:"received_at"`
}

func (Repository) Record(ctx context.Context, tx db.Executor, sc foundation.Scope, ev flow.Event, user string, selected []Version, decisions []Decision, now time.Time) error {
	raw, e := json.Marshal(ev)
	if e != nil {
		return e
	}
	for _, v := range selected {
		if _, e = tx.ExecContext(ctx, `INSERT INTO affiliate_program_facts VALUES(?,?,?,?,?,?,?,?,?,?)`, sc.Tenant, sc.App, v.ID, v.Revision, ev.Source, ev.Kind, ev.BusinessID, user, ev.At, raw); e != nil {
			return e
		}
	}
	b, e := json.Marshal(Receipt{ev.Source, ev.EventID, ev.BusinessID, user, ev.At, decisions, now})
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_program_receipts VALUES(?,?,?,?,?,?)`, sc.Tenant, sc.App, ev.Source, ev.Kind, ev.BusinessID, b)
	return e
}
func (Repository) Receipts(ctx context.Context, tx db.Executor, sc foundation.Scope) ([]Receipt, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_program_receipts WHERE tenant_id=? AND app_id=? ORDER BY JSON_UNQUOTE(JSON_EXTRACT(body,'$.received_at')) DESC,source_id,kind,business_id LIMIT 1000`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Receipt{}
	for rows.Next() {
		var raw []byte
		var r Receipt
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type Day struct {
	ProgramID string     `json:"program_id"`
	Revision  uint64     `json:"revision"`
	Daily     flow.Daily `json:"daily"`
}

func (Repository) SaveDay(ctx context.Context, tx db.Executor, sc foundation.Scope, d Day) error {
	raw, e := json.Marshal(d)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_program_days VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE body=VALUES(body)`, sc.Tenant, sc.App, d.ProgramID, d.Revision, d.Daily.Day, raw)
	return e
}
func (Repository) Days(ctx context.Context, tx db.Executor, sc foundation.Scope) ([]Day, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_program_days WHERE tenant_id=? AND app_id=? ORDER BY program_id,revision,business_day`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Day{}
	for rows.Next() {
		var raw []byte
		var d Day
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (Repository) Day(ctx context.Context, tx db.Executor, sc foundation.Scope, id string, revision uint64, day string) (Day, error) {
	var d Day
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_program_days WHERE tenant_id=? AND app_id=? AND program_id=? AND revision=? AND business_day=?`, sc.Tenant, sc.App, id, revision, day).Scan(&raw)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal(raw, &d)
	return d, e
}
