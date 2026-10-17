package adapters

import (
	"context"
	"encoding/json"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// RawFacts 只用于财务展示，包含暂停期和迟到成功订单，不把它们重新转成计奖事实。
func (Repository) RawFacts(ctx context.Context, tx db.Executor, s foundation.Scope, before time.Time) ([]affiliate.Fact, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id,body FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND business_time<? UNION ALL SELECT user_id,body FROM affiliate_late_business_facts WHERE tenant_id=? AND app_id=? AND business_time<?`, s.Tenant, s.App, before.UTC(), s.Tenant, s.App, before.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []affiliate.Fact{}
	for rows.Next() {
		var user string
		var body []byte
		var event model.Event
		if err = rows.Scan(&user, &body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &event); err != nil {
			return nil, err
		}
		out = append(out, event.Fact(user))
	}
	return out, rows.Err()
}
