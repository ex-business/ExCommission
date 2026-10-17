package domain

import (
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	"time"
)

type TeamDashboard struct {
	TodayReferrals int    `json:"today_referrals"`
	TotalReferrals int    `json:"total_referrals"`
	TodayQualified int    `json:"today_qualified"`
	TotalQualified int    `json:"total_qualified"`
	TodayDeposit   string `json:"today_deposit_minor"`
	TotalDeposit   string `json:"total_deposit_minor"`
	Earned         string `json:"total_earned_minor"`
}

// TeamDashboardFor 使用当前物理三层关系展示团队；今日存款只统计今日新注册成员，累计存款不受奖励开关影响。
func TeamDashboardFor(c affiliate.Config, user string, now time.Time, regs []affiliate.Registration, raw, eligible []affiliate.Fact, rewards []Reward) (TeamDashboard, error) {
	out := TeamDashboard{TodayDeposit: "0", TotalDeposit: "0", Earned: "0"}
	start, _ := c.Day(now)
	period, _ := c.Period(now)
	front := map[string]bool{user: true}
	members := map[string]time.Time{}
	for depth := 0; depth < 3; depth++ {
		next := map[string]bool{}
		for _, r := range regs {
			if front[r.ParentID] && !r.At.After(now) && r.UserID != user {
				if _, seen := members[r.UserID]; !seen {
					members[r.UserID] = r.At
					next[r.UserID] = true
				}
			}
		}
		front = next
	}
	grouped := map[string][]affiliate.Fact{}
	for _, f := range eligible {
		grouped[f.UserID] = append(grouped[f.UserID], f)
	}
	for id, at := range members {
		out.TotalReferrals++
		today := !at.Before(start)
		if today {
			out.TodayReferrals++
		}
		q, err := affiliate.Qualify(c.GradeCondition, grouped[id], c, now.Add(time.Microsecond))
		if err != nil {
			return out, err
		}
		if q.At != nil && !q.At.Before(period) {
			out.TotalQualified++
			if today {
				out.TodayQualified++
			}
		}
	}
	for _, f := range raw {
		at, ok := members[f.UserID]
		if !ok || f.Kind != "DEPOSIT_SUCCEEDED" || f.At.After(now) {
			continue
		}
		out.TotalDeposit = addMoney(out.TotalDeposit, f.Amount)
		if !at.Before(start) && !f.At.Before(start) {
			out.TodayDeposit = addMoney(out.TodayDeposit, f.Amount)
		}
	}
	for _, r := range rewards {
		if r.UserID == user && r.Status == "CLAIMED" {
			out.Earned = addMoney(out.Earned, r.Amount)
		}
	}
	return out, nil
}
