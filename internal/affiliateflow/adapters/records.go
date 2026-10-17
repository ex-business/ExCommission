package adapters

import (
	"context"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"strings"
)

// Records 从不可变流转快照读取同一as_of，翻页或导出期间发生领取不会更改此前查询的状态集合。
func (Repository) Records(ctx context.Context, tx db.Executor, s foundation.Scope, f model.RecordFilter, sensitive bool) (model.RecordPage, error) {
	out := model.RecordPage{Rows: []model.Reward{}, Summary: []model.RewardSummary{}, PageSummary: []model.RewardSummary{}, AsOf: f.AsOf}
	if f.Snapshot == 0 {
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence_id),0) FROM affiliate_reward_history WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App).Scan(&f.Snapshot); e != nil {
			return out, e
		}
	}
	out.Snapshot = f.Snapshot
	base := ` FROM affiliate_reward_history h JOIN(SELECT reward_id,MAX(revision) rev FROM affiliate_reward_history WHERE tenant_id=? AND app_id=? AND recorded_at<=? AND sequence_id<=? GROUP BY reward_id) v ON v.reward_id=h.reward_id AND v.rev=h.revision WHERE h.tenant_id=? AND h.app_id=? AND JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.unlocked_at'))>=? AND JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.unlocked_at'))<?`
	// 时间条件比较DATETIME而非RFC3339文本，避免小数位数量不同影响排序。
	base = strings.ReplaceAll(base, "JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.unlocked_at'))", `CAST(REPLACE(REPLACE(JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.unlocked_at')),'T',' '),'Z','') AS DATETIME(6))`)
	args := []any{s.Tenant, s.App, f.AsOf.UTC(), f.Snapshot, s.Tenant, s.App, f.From.UTC(), f.To.UTC()}
	for _, filter := range []struct{ field, value string }{{"kind", f.Kind}, {"status", f.Status}, {"user_id", f.UserID}} {
		if filter.value != "" {
			base += ` AND JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.` + filter.field + `'))=?`
			args = append(args, filter.value)
		}
	}
	amount := `CAST(JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.amount_minor')) AS DECIMAL(38,0))`
	status := `JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.status'))`
	currency := `JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.currency'))`
	summary, e := tx.QueryContext(ctx, `SELECT `+currency+`,COUNT(*),CAST(SUM(`+amount+`) AS CHAR),CAST(SUM(CASE WHEN `+status+`='CLAIMABLE' THEN `+amount+` ELSE 0 END) AS CHAR),CAST(SUM(CASE WHEN `+status+`='CLAIMED' THEN `+amount+` ELSE 0 END) AS CHAR),CAST(SUM(CASE WHEN `+status+`='PENDING_REVIEW' THEN `+amount+` ELSE 0 END) AS CHAR),CAST(SUM(CASE WHEN `+status+`='CANCELLED' THEN `+amount+` ELSE 0 END) AS CHAR)`+base+` GROUP BY `+currency+` ORDER BY `+currency, args...)
	if e != nil {
		return out, e
	}
	for summary.Next() {
		var v model.RewardSummary
		if e = summary.Scan(&v.Currency, &v.Count, &v.Total, &v.Claimable, &v.Claimed, &v.Pending, &v.Cancelled); e != nil {
			summary.Close()
			return out, e
		}
		out.Summary = append(out.Summary, v)
	}
	e = summary.Err()
	summary.Close()
	if e != nil {
		return out, e
	}
	base += ` AND h.reward_id>? ORDER BY h.reward_id LIMIT ?`
	args = append(args, f.After, f.Limit+1)
	rows, e := tx.QueryContext(ctx, `SELECT h.body`+base, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var r model.Reward
		if e = rows.Scan(&b); e != nil {
			return out, e
		}
		if e = json.Unmarshal(b, &r); e != nil {
			return out, e
		}
		if !sensitive {
			r.ClaimIP = ""
		}
		out.Rows = append(out.Rows, r)
	}
	if len(out.Rows) > f.Limit {
		out.Rows = out.Rows[:f.Limit]
		id := out.Rows[len(out.Rows)-1].ID
		out.Next = &id
	}
	out.PageSummary = model.Summarize(out.Rows)
	return out, rows.Err()
}
