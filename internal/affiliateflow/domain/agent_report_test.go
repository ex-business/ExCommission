package domain

import (
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	"testing"
	"time"
)

func TestAgentReportPreservesHistoricalLayerContributions(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	subjects := []Member{{UserID: "old", AgentID: "a-old", RegisteredAt: at}, {UserID: "new", AgentID: "a-new", RegisteredAt: at}, {UserID: "child", AgentID: "a-child", RegisteredAt: at}}
	// 旧代理已经没有直属会员，但仍应出现历史贡献；同一会员在不同业务时点可贡献不同层级。
	dailies := []Daily{{Day: "2026-10-17", Results: []FrozenCommission{{Result: affiliate.DailyCommission{UserID: "old", Amount: "30", Contributions: []affiliate.Contribution{{UserID: "child", Depth: 1, Rate: 1234, Amount: "10"}, {UserID: "child", Depth: 2, Rate: 5678, Amount: "20"}}}}}}}
	report, err := BuildAgentReport(profile(at), AgentReportFilter{From: at, To: at.Add(24 * time.Hour), Sort: "became_agent_at"}, at.Add(12*time.Hour), subjects, []affiliate.Registration{{UserID: "child", ParentID: "new", At: at}}, nil, nil, nil, map[string]Gate{}, map[string]time.Time{"old": at, "new": at}, dailies)
	if err != nil {
		t.Fatal(err)
	}
	var old *AgentReportRow
	for i := range report.Rows {
		if report.Rows[i].UserID == "old" {
			old = &report.Rows[i]
		}
	}
	if old == nil || old.Team != 0 || old.Direct != 0 || old.CalculatedCommission != "30" || len(old.Members) != 2 {
		t.Fatal(report)
	}
	for _, member := range old.Members {
		if len(member.Settlements) != 1 || (member.Depth == 1 && member.Settlements[0].Rate != 1234) || (member.Depth == 2 && member.Settlements[0].Rate != 5678) {
			t.Fatal("冻结费率未按历史层级保留", member)
		}
		if !member.Historical {
			t.Fatal("迁移后的历史贡献混入当前团队", member)
		}
	}
	AddProvisionalContribution(old, "child", 1, "3")
	AddProvisionalContribution(old, "child", 2, "4")
	if old.Members[0].Estimate != "3" || old.Members[1].Estimate != "4" {
		t.Fatal("不同层级贡献重复累加", old.Members)
	}
}
