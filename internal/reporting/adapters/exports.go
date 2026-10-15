package adapters

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/reporting/domain"
	"time"
)

type Exports struct{}

func (Exports) Save(ctx context.Context, tx db.Executor, s foundation.Scope, id, subject string, filters model.Filters, report model.Report, csv []byte, hash string, now, expires time.Time) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	query, err := json.Marshal(filters)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reporting_exports VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, id, subject, report.Kind, query, body, csv, hash, now, expires)
	return err
}
func (Exports) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string, now time.Time) (model.Export, error) {
	var e model.Export
	var snapshot []byte
	err := tx.QueryRowContext(ctx, `SELECT export_id,kind,snapshot,csv_body,snapshot_hash,expires_at FROM reporting_exports WHERE tenant_id=? AND app_id=? AND export_id=?`, s.Tenant, s.App, id).Scan(&e.ID, &e.Kind, &snapshot, &e.CSV, &e.Hash, &e.Expires)
	if errors.Is(err, sql.ErrNoRows) {
		return e, errors.New("NOT_FOUND")
	}
	if err != nil {
		return e, err
	}
	if !now.Before(e.Expires) {
		var info struct {
			AsOf time.Time `json:"as_of"`
		}
		_ = json.Unmarshal(snapshot, &info)
		e.AsOf = info.AsOf
		e.Status = "EXPIRED"
		e.CSV = nil
		return e, nil
	}
	var report model.Report
	if err = json.Unmarshal(snapshot, &report); err != nil {
		return e, err
	}
	sum := sha256.Sum256(append(append([]byte{}, snapshot...), e.CSV...))
	if hex.EncodeToString(sum[:]) != e.Hash {
		return e, errors.New("SNAPSHOT_INTEGRITY_FAILED")
	}
	e.AsOf = report.AsOf
	e.Status = "READY"
	if !now.Before(e.Expires) {
		e.Status = "EXPIRED"
		e.CSV = nil
	}
	return e, nil
}
