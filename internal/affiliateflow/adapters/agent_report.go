package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	afdomain "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	sites "github.com/ex-business/ExCommission/internal/sites/adapters"
	"math/big"
	"strings"
	"time"
)

type AgentReports struct{ DB *sql.DB }

// Build 在只读可重复读事务中装配事实、关系与奖励，避免当前日跨表读取混入不同提交版本。
func (r AgentReports) Build(ctx context.Context, s foundation.Scope, f model.AgentReportFilter, now time.Time) (model.AgentReport, error) {
	empty := model.AgentReport{}
	tx, e := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return empty, e
	}
	defer tx.Rollback()
	cutoff := f.To
	if cutoff.After(now.Add(time.Microsecond)) {
		cutoff = now.Add(time.Microsecond)
	}
	v, e := (affiliate.Repository{}).At(ctx, tx, s, cutoff.Add(-time.Microsecond))
	if e != nil {
		return empty, e
	}
	members, e := (Members{}).List(ctx, tx, s, cutoff.Add(-time.Microsecond))
	if e != nil {
		return empty, e
	}
	relations, e := (Relations{}).Close(ctx, tx, s, cutoff.Add(-time.Microsecond))
	if e != nil {
		return empty, e
	}
	facts, e := (Repository{}).Facts(ctx, tx, s, cutoff)
	if e != nil {
		return empty, e
	}
	raw, e := (Repository{}).RawFacts(ctx, tx, s, cutoff)
	if e != nil {
		return empty, e
	}
	var rows *sql.Rows
	gates := map[string]model.Gate{}
	for _, m := range members {
		g, e := (Repository{}).GateAt(ctx, tx, s, m.UserID, m.RegisteredAt, cutoff.Add(-time.Microsecond))
		if e != nil {
			return empty, e
		}
		gates[m.UserID] = g
	}
	promoted := map[string]time.Time{}
	rows, e = tx.QueryContext(ctx, `SELECT a.user_id,MIN(r.effective_from) FROM agent_relations r JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.app_id=r.app_id AND a.agent_id=r.parent_id WHERE r.tenant_id=? AND r.app_id=? AND r.effective_from<? GROUP BY a.user_id`, s.Tenant, s.App, cutoff.UTC())
	if e != nil {
		return empty, e
	}
	for rows.Next() {
		var user string
		var at time.Time
		if e = rows.Scan(&user, &at); e != nil {
			rows.Close()
			return empty, e
		}
		promoted[user] = at
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return empty, e
	}
	dailies := []model.Daily{}
	rows, e = tx.QueryContext(ctx, `SELECT body FROM affiliate_daily_runs WHERE tenant_id=? AND app_id=? AND business_day>=? AND business_day<? ORDER BY business_day`, s.Tenant, s.App, f.From.In(v.Config.Location()).Format("2006-01-02"), f.To.In(v.Config.Location()).Format("2006-01-02"))
	if e != nil {
		return empty, e
	}
	for rows.Next() {
		var b []byte
		var d model.Daily
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return empty, e
		}
		if e = json.Unmarshal(b, &d); e != nil {
			rows.Close()
			return empty, e
		}
		dailies = append(dailies, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return empty, e
	}
	rewards := []model.Reward{}
	rows, e = tx.QueryContext(ctx, `SELECT body FROM affiliate_business_rewards WHERE tenant_id=? AND app_id=? AND claimed_at>=? AND claimed_at<?`, s.Tenant, s.App, f.From.UTC(), cutoff.UTC())
	if e != nil {
		return empty, e
	}
	for rows.Next() {
		var b []byte
		var reward model.Reward
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return empty, e
		}
		if e = json.Unmarshal(b, &reward); e != nil {
			rows.Close()
			return empty, e
		}
		rewards = append(rewards, reward)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return empty, e
	}
	report, e := model.BuildAgentReport(v, f, now, members, relations, raw, facts, rewards, gates, promoted, dailies)
	if e != nil {
		return empty, e
	}
	if report.Provisional {
		start, end := v.Config.Day(now)
		// 跨日风控按历史归属竞争，代理重开不重选全站赢家；与正式日结保持同一输入口径。
		riskStart := start
		if len(v.Config.RiskRules) > 0 {
			riskStart = time.Time{}
		}
		ownership, e := (Relations{}).AttributionsAtDepth(ctx, tx, s, facts, riskStart, end, v.Config.DepthLimit())
		if e != nil {
			return empty, e
		}
		site, e := (sites.Repository{}).Current(ctx, tx, s, false)
		if e != nil {
			return empty, e
		}
		for n := range report.Rows {
			row := &report.Rows[n]
			g := gates[row.UserID]
			if !g.Enabled || !v.Config.Enabled || (site.Configured && !site.Enabled) {
				continue
			}
			selected := []afdomain.Fact{}
			for _, fact := range facts {
				if !fact.At.Before(g.Since) {
					selected = append(selected, fact)
				}
			}
			calculation, e := afdomain.CalculateRiskDay(v.Config, row.UserID, now, relations, selected, ownership, facts)
			if e != nil {
				return empty, e
			}
			row.Estimate = calculation.Amount
			for _, c := range calculation.Contributions {
				model.AddProvisionalContribution(row, c.UserID, c.Depth, c.Amount)
			}

		}
	}
	profiles, e := ReadMemberProfiles(ctx, tx, s)
	if e != nil {
		return empty, e
	}
	for i := range report.Rows {
		report.Rows[i].Profile = profiles[report.Rows[i].UserID]
		for n := range report.Rows[i].Members {
			report.Rows[i].Members[n].Profile = profiles[report.Rows[i].Members[n].UserID]
		}
	}
	filtered := []model.AgentReportRow{}
	for _, row := range report.Rows {
		if f.Search != "" && !strings.Contains(strings.ToLower(row.Profile.Name+" "+row.Profile.DisplayID+" "+row.UserID), strings.ToLower(f.Search)) {
			continue
		}
		if f.State == "enabled" && !row.Enabled || f.State == "disabled" && row.Enabled {
			continue
		}
		if f.LoginIP != "" && f.LoginIP != row.Profile.LastIP {
			continue
		}
		filtered = append(filtered, row)
	}
	report.Rows = filtered
	if e = tx.Commit(); e != nil {
		return empty, e
	}
	return report, nil
}
func (AgentReports) Save(ctx context.Context, tx db.Executor, s foundation.Scope, id, subject string, report model.AgentReport, now time.Time) error {
	b, e := json.Marshal(report)
	if e != nil {
		return e
	}
	if len(b) > 8<<20 {
		return errors.New("REPORT_TOO_LARGE")
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM affiliate_report_snapshots WHERE tenant_id=? AND app_id=? AND expires_at<=?`, s.Tenant, s.App, now.UTC()); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_report_snapshots VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, id, subject, b, now.UTC(), now.Add(10*time.Minute).UTC())
	return e
}
func (AgentReports) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id, subject string, now time.Time) (model.AgentReport, error) {
	var out model.AgentReport
	var owner string
	var expiry time.Time
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT subject_id,body,expires_at FROM affiliate_report_snapshots WHERE tenant_id=? AND app_id=? AND snapshot_id=?`, s.Tenant, s.App, id).Scan(&owner, &b, &expiry)
	if errors.Is(e, sql.ErrNoRows) {
		return out, errors.New("NOT_FOUND")
	}
	if e != nil {
		return out, e
	}
	if owner != subject {
		return out, errors.New("PERMISSION_DENIED")
	}
	if !now.Before(expiry) {
		return out, errors.New("REPORT_EXPIRED")
	}
	e = json.Unmarshal(b, &out)
	return out, e
}

func addReportMoney(a, b string) string {
	x, _ := new(big.Int).SetString(a, 10)
	y, _ := new(big.Int).SetString(b, 10)
	return x.Add(x, y).String()
}
