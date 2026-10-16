package application

import (
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"testing"
	"time"
)

func TestDayEndPublicationAtCustomBoundary(t *testing.T) {
	config := plan.Config{AgentPolicy: plan.Policy{GradeWindow: "BUSINESS_DAY", DayEnd: &plan.DayEndPolicy{Timezone: "Asia/Shanghai"}}}
	at := time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)
	if err := dayBoundary(config, at); err != nil {
		t.Fatal(err)
	}
	if err := dayBoundary(config, at.Add(time.Hour)); err == nil {
		t.Fatal("日中发布不应通过")
	}
	config.AgentPolicy.GradeWindow = "PREVIOUS_CALENDAR_MONTH"
	if err := dayBoundary(config, at.Add(time.Hour)); err != nil {
		t.Fatal("旧月度策略应保持原时点规则", err)
	}
}
