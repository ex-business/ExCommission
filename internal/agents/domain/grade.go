package domain

import "time"

type GradeState struct {
	Grade       string     `json:"grade_id"`
	Eligibility string     `json:"eligibility"`
	Revision    uint64     `json:"revision"`
	From        time.Time  `json:"effective_from"`
	To          *time.Time `json:"effective_to"`
}
type GradeChange struct {
	AgentID     string `json:"agent_id"`
	PlanID      string `json:"plan_id"`
	GradeID     string `json:"grade_id"`
	Eligibility string `json:"eligibility"`
	Revision    uint64 `json:"expected_revision"`
	Reason      string `json:"reason"`
}
