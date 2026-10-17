package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"strings"
	"time"
)

type Repository struct{}

func (r Repository) Bootstrap(ctx context.Context, tx db.Executor, s foundation.Scope) error {
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	catalog := foundation.CurrencyCatalog{Version: "currency-v1", From: epoch, Currencies: []foundation.Currency{{Code: "USD", Exponent: 2, Format: "0.00", Channels: []string{"sandbox"}}, {Code: "BTC", Exponent: 8, Format: "0.00000000", Channels: []string{"sandbox"}}, {Code: "POINT", Exponent: 0, Format: "0", Channels: []string{"sandbox"}}}}
	calendar := foundation.Calendar{Version: "UTC-v1", From: epoch, Timezone: "UTC", Period: "DAILY"}
	for _, v := range []struct {
		kind, id string
		body     any
	}{{"CURRENCY", "currency-v1", catalog}, {"CALENDAR", "UTC-v1", calendar}} {
		body, _ := json.Marshal(v.body)
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO platform_catalog_versions VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, v.kind, v.id, epoch, body); err != nil {
			return err
		}
	}
	for _, v := range [][2]string{{"SOURCE", "sandbox"}, {"PAYMENT", "sandbox"}, {"NOTIFICATION", "local-inbox"}} {
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO platform_component_states VALUES(?,?,?,?,TRUE,0)`, s.Tenant, s.App, v[0], v[1]); err != nil {
			return err
		}
	}
	return nil
}
func (Repository) CurrencyAt(ctx context.Context, tx db.Executor, s foundation.Scope, code string, at time.Time) (string, int, error) {
	var body []byte
	var version string
	err := tx.QueryRowContext(ctx, `SELECT version_id,body FROM platform_catalog_versions WHERE tenant_id=? AND app_id=? AND catalog_type='CURRENCY' AND effective_from<=? ORDER BY effective_from DESC LIMIT 1`, s.Tenant, s.App, at).Scan(&version, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, errors.New("UNKNOWN_FACT")
	}
	if err != nil {
		return "", 0, err
	}
	var catalog foundation.CurrencyCatalog
	if err = json.Unmarshal(body, &catalog); err != nil {
		return "", 0, err
	}
	for _, currency := range catalog.Currencies {
		if currency.Code == code {
			return version, currency.Exponent, nil
		}
	}
	return "", 0, errors.New("UNSUPPORTED_CAPABILITY")
}
func (Repository) CalendarVersions(ctx context.Context, tx db.Executor, s foundation.Scope) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version_id FROM platform_catalog_versions WHERE tenant_id=? AND app_id=? AND catalog_type='CALENDAR'`, s.Tenant, s.App)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
func (Repository) Save(ctx context.Context, tx db.Executor, s foundation.Scope, kind, id string, from time.Time, body []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO platform_catalog_versions VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, kind, id, from, body)
	return err
}
func (Repository) Component(ctx context.Context, tx db.Executor, s foundation.Scope, kind, id string) (bool, uint64, error) {
	var enabled bool
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT enabled,revision FROM platform_component_states WHERE tenant_id=? AND app_id=? AND component_type=? AND component_id=?`, s.Tenant, s.App, kind, id).Scan(&enabled, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, errors.New("UNSUPPORTED_CAPABILITY")
	}
	return enabled, revision, err
}
func (Repository) SetComponent(ctx context.Context, tx db.Executor, s foundation.Scope, c foundation.ComponentState, revision uint64) error {
	res, err := tx.ExecContext(ctx, `UPDATE platform_component_states SET enabled=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND component_type=? AND component_id=? AND revision=?`, c.Enabled, s.Tenant, s.App, c.Type, c.ID, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("STALE_APPROVAL")
	}
	return nil
}
func (Repository) Versions(ctx context.Context, tx db.Executor, s foundation.Scope) ([]json.RawMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT body FROM platform_catalog_versions WHERE tenant_id=? AND app_id=? ORDER BY catalog_type,effective_from`, s.Tenant, s.App)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(body))
	}
	return out, rows.Err()
}
func DisplayFormat(exponent int) string {
	if exponent == 0 {
		return "0"
	}
	return "0." + strings.Repeat("0", exponent)
}

func (Repository) ValidateCalendar(ctx context.Context, tx db.Executor, s foundation.Scope, version string, period time.Time) error {
	var selected string
	err := tx.QueryRowContext(ctx, `SELECT version_id FROM platform_catalog_versions WHERE tenant_id=? AND app_id=? AND catalog_type='CALENDAR' AND effective_from<=? ORDER BY effective_from DESC LIMIT 1`, s.Tenant, s.App, period).Scan(&selected)
	if err != nil {
		return err
	}
	if selected != version {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	return nil
}

// CalendarForPeriod selects the already registered calendar at the period boundary.
func (Repository) CalendarForPeriod(ctx context.Context, tx db.Executor, s foundation.Scope, period time.Time) (string, error) {
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT body FROM platform_catalog_versions WHERE tenant_id=? AND app_id=? AND catalog_type='CALENDAR' AND effective_from<=? ORDER BY effective_from DESC LIMIT 1`, s.Tenant, s.App, period).Scan(&body)
	if err != nil {
		return "", err
	}
	var c foundation.Calendar
	if json.Unmarshal(body, &c) != nil || c.Timezone != "UTC" || c.Period != "DAILY" || c.Version == "" {
		return "", errors.New("UNSUPPORTED_CAPABILITY")
	}
	return c.Version, nil
}
