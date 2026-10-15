package domain

import "time"

// Contract 只保存合作事实和方案引用，禁止携带佣金费率覆盖。
type Contract struct {
	ID, AgentID, PlanID, Evidence string
	From                          time.Time
	To                            *time.Time
}
