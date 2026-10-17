package adapters

import (
	"context"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"strconv"
	"time"
)

// CreditAffiliate 让新业务复用原生平衡账簿。配置引用带类型前缀，指向 affiliate_config_versions，不能伪装成旧方案版本。
func (r Repository) CreditAffiliate(ctx context.Context, tx db.Executor, s foundation.Scope, reward, agent, kind, currency, amount string, revision uint64, at, now time.Time) (string, error) {
	if !foundation.ValidID(reward) || !foundation.ValidID(agent) || revision == 0 || (kind != "REFERRAL" && kind != "INVITEE" && kind != "COMMISSION") {
		return "", foundation.ErrValidation
	}
	rule := stableID("affiliate", kind)
	version := "affiliate-config-v" + strconv.FormatUint(revision, 10)
	task := calc.Task{ID: reward, Source: "affiliate-business", BusinessID: reward, EventType: "AFF_" + kind, BusinessTime: at}
	in := calc.Input{TaskID: reward, PlanVersionID: version, Plan: plan.Config{Currency: currency}, Facts: calc.Facts{BusinessTime: at}}
	result := calc.Result{TaskID: reward, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: rule, BeneficiaryID: agent, Currency: currency, Minor: amount, Algorithm: "AFFILIATE_SNAPSHOT", Version: "1"}}}
	if e := r.Commit(ctx, tx, s, task, in, result, now); e != nil {
		return "", e
	}
	return stableID(task.Source, task.BusinessID, task.EventType, version, rule, agent, currency), nil
}
