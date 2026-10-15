package adapters

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	model "github.com/ex-business/ExCommission/internal/reporting/domain"
	"math/big"
	"sort"
	"time"
)

type Reader struct{ DB *sql.DB }

func ValidateFilters(f model.Filters) error {
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return foundation.ErrValidation
	}
	for _, id := range []string{f.PlanID, f.VersionID, f.AgentID, f.Currency} {
		if id != "" && !foundation.ValidID(id) {
			return foundation.ErrValidation
		}
	}
	return nil
}
func (r Reader) Read(ctx context.Context, s foundation.Scope, kind string, f model.Filters) (model.Report, error) {
	out := model.Report{Kind: kind, Rows: []map[string]string{}, Catalog: "currency-v1", Metrics: map[string]string{"profit": "UNAVAILABLE_NO_REVENUE_SOURCE", "conversion_rate": "UNAVAILABLE_NO_ATTRIBUTION_SOURCE"}}
	if !s.Valid() || (kind != "earnings" && kind != "finance" && kind != "cost") {
		return out, foundation.ErrValidation
	}
	if err := ValidateFilters(f); err != nil {
		return out, err
	}
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	// Establish one consistent read view for details and watermark.
	var count int
	var latest sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),MAX(posted_at) FROM ledger_journals WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App).Scan(&count, &latest); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&out.AsOf); err != nil {
		return out, err
	}
	watermark, _ := json.Marshal(struct {
		Count  int
		Latest sql.NullTime
	}{count, latest})
	sum := sha256.Sum256(watermark)
	out.Watermark = hex.EncodeToString(sum[:])
	var catalog string
	err = tx.QueryRowContext(ctx, `SELECT version_id FROM platform_catalog_versions WHERE tenant_id=? AND app_id=? AND catalog_type='CURRENCY' AND effective_from<=? ORDER BY effective_from DESC LIMIT 1`, s.Tenant, s.App, out.AsOf).Scan(&catalog)
	if err == nil {
		out.Catalog = catalog
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	if kind != "earnings" && (f.PlanID != "" || f.VersionID != "" || f.AgentID != "") {
		q := `SELECT COUNT(*) FROM ledger_journals j LEFT JOIN ledger_journal_links l ON l.tenant_id=j.tenant_id AND l.app_id=j.app_id AND l.journal_id=j.journal_id LEFT JOIN ledger_entitlements e ON e.tenant_id=l.tenant_id AND e.app_id=l.app_id AND e.entitlement_id=l.entitlement_id LEFT JOIN plan_versions v ON v.tenant_id=e.tenant_id AND v.app_id=e.app_id AND v.version_id=e.plan_version_id WHERE j.tenant_id=? AND j.app_id=? AND (l.journal_id IS NULL OR e.entitlement_id IS NULL`
		if f.PlanID != "" {
			q += " OR v.version_id IS NULL"
		}
		q += ")"
		args := []any{s.Tenant, s.App}
		if f.From != nil {
			q += " AND j.occurred_at>=?"
			args = append(args, *f.From)
		}
		if f.To != nil {
			q += " AND j.occurred_at<?"
			args = append(args, *f.To)
		}
		if f.Currency != "" {
			q += " AND j.currency=?"
			args = append(args, f.Currency)
		}
		var gaps int
		if err = tx.QueryRowContext(ctx, q, args...).Scan(&gaps); err != nil {
			return out, err
		}
		if gaps > 0 {
			return out, errors.New("REPORT_INCOMPLETE_LINEAGE")
		}
	}
	q := `SELECT e.entitlement_id,e.task_id,e.beneficiary_id,e.currency,CAST(e.target AS CHAR),CAST(e.pending AS CHAR),CAST(e.available AS CHAR),CAST(e.reserved AS CHAR),CAST(e.paid AS CHAR),CAST(e.debt AS CHAR),CAST(e.recovered AS CHAR),e.business_time,e.plan_version_id,COALESCE(v.plan_id,'') FROM ledger_entitlements e LEFT JOIN plan_versions v ON v.tenant_id=e.tenant_id AND v.app_id=e.app_id AND v.version_id=e.plan_version_id WHERE e.tenant_id=? AND e.app_id=?`
	args := []any{s.Tenant, s.App}
	timeColumn := "e.business_time"
	if kind != "earnings" {
		q = `SELECT j.journal_id,j.business_ref,j.currency,j.debit_account,j.credit_account,CAST(j.amount AS CHAR),j.occurred_at,COALESCE(e.entitlement_id,''),COALESCE(e.beneficiary_id,''),COALESCE(e.plan_version_id,''),COALESCE(v.plan_id,'') FROM ledger_journals j LEFT JOIN ledger_journal_links l ON l.tenant_id=j.tenant_id AND l.app_id=j.app_id AND l.journal_id=j.journal_id LEFT JOIN ledger_entitlements e ON e.tenant_id=l.tenant_id AND e.app_id=l.app_id AND e.entitlement_id=l.entitlement_id LEFT JOIN plan_versions v ON v.tenant_id=e.tenant_id AND v.app_id=e.app_id AND v.version_id=e.plan_version_id WHERE j.tenant_id=? AND j.app_id=?`
		timeColumn = "j.occurred_at"
	}
	if f.From != nil {
		q += " AND " + timeColumn + ">=?"
		args = append(args, *f.From)
	}
	if f.To != nil {
		q += " AND " + timeColumn + "<?"
		args = append(args, *f.To)
	}
	currencyColumn := "e.currency"
	if kind != "earnings" {
		currencyColumn = "j.currency"
	}
	for _, filter := range []struct{ column, value string }{{"v.plan_id", f.PlanID}, {"e.plan_version_id", f.VersionID}, {"e.beneficiary_id", f.AgentID}, {currencyColumn, f.Currency}} {
		if filter.value != "" {
			q += " AND " + filter.column + "=?"
			args = append(args, filter.value)
		}
	}
	orderID := "e.entitlement_id"
	if kind != "earnings" {
		orderID = "j.journal_id"
	}
	q += " ORDER BY " + timeColumn + "," + orderID + " LIMIT 10001"
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return out, err
	}
	cost := map[string]*big.Int{}
	for rows.Next() {
		row := map[string]string{}
		if kind == "earnings" {
			var id, task, agent, currency, target, pending, available, reserved, paid, debt, recovered, version, plan string
			var at time.Time
			err = rows.Scan(&id, &task, &agent, &currency, &target, &pending, &available, &reserved, &paid, &debt, &recovered, &at, &version, &plan)
			row = map[string]string{"entitlement_id": id, "task_id": task, "agent_id": agent, "currency": currency, "net_minor": target, "pending_minor": pending, "available_minor": available, "reserved_minor": reserved, "paid_minor": paid, "debt_minor": debt, "recovered_minor": recovered, "business_time": at.Format(time.RFC3339Nano), "version_id": version, "plan_id": plan}
		} else {
			var id, ref, currency, debit, credit, amount, entitlement, agent, version, plan string
			var at time.Time
			err = rows.Scan(&id, &ref, &currency, &debit, &credit, &amount, &at, &entitlement, &agent, &version, &plan)
			row = map[string]string{"journal_id": id, "business_ref": ref, "currency": currency, "debit_account": debit, "credit_account": credit, "minor_units": amount, "occurred_at": at.Format(time.RFC3339Nano), "entitlement_id": entitlement, "agent_id": agent, "version_id": version, "plan_id": plan}
			if cost[currency] == nil {
				cost[currency] = new(big.Int)
			}
			n, _ := new(big.Int).SetString(amount, 10)
			if debit == "COMMISSION_EXPENSE" {
				cost[currency].Add(cost[currency], n)
			}
			if credit == "COMMISSION_EXPENSE" {
				cost[currency].Sub(cost[currency], n)
			}
		}
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Rows = append(out.Rows, row)
		if len(out.Rows) > 10000 {
			rows.Close()
			return out, errors.New("REPORT_TOO_LARGE")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if kind == "cost" {
		out.Rows = []map[string]string{}
		currencies := []string{}
		for code := range cost {
			currencies = append(currencies, code)
		}
		sort.Strings(currencies)
		for _, code := range currencies {
			out.Rows = append(out.Rows, map[string]string{"currency": code, "net_commission_cost_minor": cost[code].String(), "profit": "UNAVAILABLE_NO_REVENUE_SOURCE"})
		}
	}
	return out, tx.Commit()
}
