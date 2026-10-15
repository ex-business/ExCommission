package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"time"
)

type Repository struct{}

func stableID(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validAmount(s string) bool {
	n, ok := new(big.Int).SetString(s, 10)
	return ok && n.Sign() > 0 && n.String() == s && len(s) <= 38
}

// Commit runs inside the worker's transaction; task fencing failure rolls back every posting.
func (Repository) Commit(ctx context.Context, tx db.Executor, scope foundation.Scope, task calc.Task, input calc.Input, result calc.Result, now time.Time) error {
	if !scope.Valid() || task.ID != input.TaskID || result.TaskID != task.ID || input.PlanVersionID == "" || (result.Status != "SUCCEEDED" && result.Status != "NO_MATCH") {
		return foundation.ErrValidation
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if !validAmount(item.Minor) || !foundation.ValidID(item.BeneficiaryID) || !foundation.ValidID(item.RuleID) || item.Currency != input.Plan.Currency {
			return foundation.ErrValidation
		}
		originalAmount := item.Minor
		if item.OriginalMinor != "" {
			originalAmount = item.OriginalMinor
			n, ok := new(big.Int).SetString(originalAmount, 10)
			target, _ := new(big.Int).SetString(item.Minor, 10)
			if !ok || !validAmount(originalAmount) || n.Cmp(target) < 0 {
				return foundation.ErrValidation
			}
		}
		id := stableID(task.Source, task.BusinessID, task.EventType, input.PlanVersionID, item.RuleID, item.BeneficiaryID, item.Currency)
		if seen[id] {
			return foundation.ErrValidation
		}
		seen[id] = true
		var oldTask, oldAmount string
		rows, err := tx.QueryContext(ctx, `SELECT task_id,CAST(original_target AS CHAR) FROM ledger_entitlements WHERE tenant_id=? AND app_id=? AND entitlement_id=? FOR UPDATE`, scope.Tenant, scope.App, id)
		if err != nil {
			return err
		}
		exists := rows.Next()
		if exists {
			err = rows.Scan(&oldTask, &oldAmount)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if exists {
			if oldTask != task.ID || oldAmount != originalAmount {
				return errors.New("LEDGER_IDEMPOTENCY_CONFLICT")
			}
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ledger_entitlements(tenant_id,app_id,entitlement_id,task_id,source_id,business_id,event_type,plan_version_id,rule_id,beneficiary_id,currency,original_target,target,pending,business_time,posted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, scope.Tenant, scope.App, id, task.ID, task.Source, task.BusinessID, task.EventType, input.PlanVersionID, item.RuleID, item.BeneficiaryID, item.Currency, originalAmount, item.Minor, item.Minor, input.Facts.BusinessTime, now)
		if err != nil {
			return err
		}
		minimum := input.Plan.Settlement.Minimum
		if minimum == "" {
			minimum = "0"
		}
		min, ok := new(big.Int).SetString(minimum, 10)
		if !ok || min.Sign() < 0 || len(minimum) > 38 {
			return foundation.ErrValidation
		}
		if input.Plan.Settlement.Maximum != nil {
			max, ok := new(big.Int).SetString(*input.Plan.Settlement.Maximum, 10)
			if !ok || max.Sign() < 0 || len(*input.Plan.Settlement.Maximum) > 38 {
				return foundation.ErrValidation
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ledger_payout_policies VALUES(?,?,?,?,?)`, scope.Tenant, scope.App, id, minimum, input.Plan.Settlement.Maximum); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ledger_journals(tenant_id,app_id,journal_id,business_ref,idempotency_key,currency,debit_account,credit_account,amount,occurred_at,posted_at) VALUES(?,?,?,?,?,?, 'COMMISSION_EXPENSE','PAYABLE_PENDING',?,?,?)`, scope.Tenant, scope.App, id, id, id, item.Currency, item.Minor, input.Facts.BusinessTime, now)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ledger_journal_links VALUES(?,?,?,?)`, scope.Tenant, scope.App, id, id); err != nil {
			return err
		}
		frozen := false
		for _, a := range append(append([]calc.Actor{}, input.Ancestors...), input.Recipients...) {
			if a.ID == item.BeneficiaryID && a.Status == "FROZEN" {
				frozen = true
			}
		}
		if frozen {
			_, err = tx.ExecContext(ctx, `INSERT INTO ledger_restrictions(tenant_id,app_id,entitlement_id,risk_id,reason_code,created_at) VALUES(?,?,?,?,?,?)`, scope.Tenant, scope.App, id, "snapshot-frozen-"+id[:32], "AGENT_FROZEN", now)
			if err != nil {
				return err
			}
		}
		body, _ := json.Marshal(map[string]string{"entitlement_id": id, "task_id": task.ID, "currency": item.Currency, "minor_units": item.Minor})
		if err = mysql.AppendOutbox(ctx, tx, mysql.OutboxEvent{Scope: scope, ID: id, Topic: "entitlement.created", Payload: body, CreatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
