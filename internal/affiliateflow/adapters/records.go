package adapters

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"strings"
	"time"
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
	timeField := "unlocked_at"
	if f.TimeField == "claimed_at" {
		timeField = "claimed_at"
	}
	if timeField != "unlocked_at" {
		base = strings.ReplaceAll(base, "$.unlocked_at", "$."+timeField)
	}
	args := []any{s.Tenant, s.App, f.AsOf.UTC(), f.Snapshot, s.Tenant, s.App, f.From.UTC(), f.To.UTC()}
	for _, filter := range []struct{ field, value string }{{"business_day", f.BusinessDay}, {"kind", f.Kind}, {"status", f.Status}, {"user_id", f.UserID}, {"reward_id", f.RewardID}, {"claim_id", f.ClaimID}, {"claim_ip", f.ClaimIP}} {
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
	// 新排序固定金额、时间及reward_id并列次序；旧请求继续使用ID升序，汇总始终在游标过滤前计算。
	type recordCursor struct{ Sort, Field, Value, Time, ID string }
	timestamp := `CAST(REPLACE(REPLACE(JSON_UNQUOTE(JSON_EXTRACT(h.body,'$.` + timeField + `')),'T',' '),'Z','') AS DATETIME(6))`
	order := "h.reward_id"
	if f.Sort != "" {
		order = timestamp + ` DESC,h.reward_id ASC`
		if f.Sort == "amount_desc" {
			order = amount + ` DESC,` + order
		}
		if f.After != "" {
			raw, err := base64.RawURLEncoding.DecodeString(f.After)
			var cursor recordCursor
			if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Sort != f.Sort || cursor.Field != timeField || !foundation.ValidID(cursor.ID) {
				return out, foundation.ErrValidation
			}
			at, err := time.Parse(time.RFC3339Nano, cursor.Time)
			if err != nil {
				return out, foundation.ErrValidation
			}
			stamp := at.UTC().Format("2006-01-02 15:04:05.999999")
			if f.Sort == "amount_desc" {
				n, ok := new(big.Int).SetString(cursor.Value, 10)
				if !ok || n.Sign() < 0 || len(cursor.Value) > 38 {
					return out, foundation.ErrValidation
				}
				base += ` AND (` + amount + `<? OR (` + amount + `=? AND (` + timestamp + `<? OR (` + timestamp + `=? AND h.reward_id>?))))`
				args = append(args, cursor.Value, cursor.Value, stamp, stamp, cursor.ID)
			} else {
				base += ` AND (` + timestamp + `<? OR (` + timestamp + `=? AND h.reward_id>?))`
				args = append(args, stamp, stamp, cursor.ID)
			}
		}
	} else {
		base += ` AND h.reward_id>?`
		args = append(args, f.After)
	}
	base += ` ORDER BY ` + order + ` LIMIT ?`
	args = append(args, f.Limit+1)
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
		last := out.Rows[len(out.Rows)-1]
		id := last.ID
		if f.Sort != "" {
			at := last.UnlockedAt
			if timeField == "claimed_at" {
				if last.ClaimedAt == nil {
					return out, fmt.Errorf("领取时间游标缺失")
				}
				at = *last.ClaimedAt
			}
			raw, _ := json.Marshal(recordCursor{Sort: f.Sort, Field: timeField, Value: last.Amount, Time: at.UTC().Format(time.RFC3339Nano), ID: id})
			id = base64.RawURLEncoding.EncodeToString(raw)
		}
		out.Next = &id
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	profiles, e := ReadMemberProfiles(ctx, tx, s)
	if e != nil {
		return out, e
	}
	for i := range out.Rows {
		profile := profiles[out.Rows[i].UserID]
		if !sensitive {
			profile.LastIP = ""
		}
		out.Rows[i].Profile = &profile
	}
	out.PageSummary = model.Summarize(out.Rows)
	return out, rows.Err()
}
