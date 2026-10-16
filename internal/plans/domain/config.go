package domain

import "encoding/json"

type Route struct {
	Product   string `json:"product_code"`
	EventType string `json:"event_type"`
	Scene     string `json:"scene"`
}
type Scope struct {
	Type     string   `json:"type"`
	AgentIDs []string `json:"agent_ids"`
}

// DayEndPolicy 随发布版本冻结，统计层级可独立选择，不改变实际返佣层级。
type DayEndPolicy struct {
	Timezone              string `json:"timezone"`
	SourceID              string `json:"source_id"`
	QualificationPolicyID string `json:"qualification_policy_id"`
	MemberDepths          []int  `json:"member_depths"`
	VolumeDepths          []int  `json:"volume_depths"`
}
type Policy struct {
	DayEnd              *DayEndPolicy `json:"day_end,omitempty"`
	MaxDepth            int           `json:"max_depth"`
	SelfReward          bool          `json:"self_reward"`
	EarnFrozen          bool          `json:"earn_while_frozen"`
	RequireContract     bool          `json:"require_contract"`
	GradeWindow         string        `json:"grade_window"`
	GradeEvaluation     string        `json:"grade_evaluation"`
	EligibilityRequired bool          `json:"eligibility_required"`
}
type Condition struct {
	Metric   string          `json:"metric,omitempty"`
	Operator string          `json:"operator,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
	Unit     string          `json:"unit,omitempty"`
	Logic    string          `json:"logic,omitempty"`
	Children []Condition     `json:"children,omitempty"`
}
type GradeCondition struct {
	Metric   string `json:"metric"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
	Unit     string `json:"unit"`
}
type Grade struct {
	ID         string           `json:"id"`
	Rank       int              `json:"rank"`
	Conditions []GradeCondition `json:"conditions"`
}
type Rate struct {
	Product string `json:"product_code"`
	Grade   string `json:"grade_id"`
	Depth   *int   `json:"depth,omitempty"`
	PPM     string `json:"rate_ppm"`
}
type Tier struct {
	Lower string  `json:"lower"`
	Upper *string `json:"upper"`
	PPM   string  `json:"rate_ppm"`
}
type Weight struct {
	BeneficiaryID string `json:"beneficiary_id"`
	Weight        string `json:"weight"`
}
type Parameters struct {
	RatePPM        string   `json:"rate_ppm,omitempty"`
	AmountMinor    string   `json:"amount_minor,omitempty"`
	RewardCurrency string   `json:"reward_currency,omitempty"`
	UnitPrice      string   `json:"unit_price,omitempty"`
	QuantityUnit   string   `json:"quantity_unit,omitempty"`
	Rates          []Rate   `json:"rates,omitempty"`
	Tiers          []Tier   `json:"tiers,omitempty"`
	PoolMinor      string   `json:"pool_minor,omitempty"`
	Beneficiaries  []Weight `json:"beneficiaries,omitempty"`
}
type Rule struct {
	ID             string     `json:"id"`
	Algorithm      string     `json:"algorithm_id"`
	Version        string     `json:"algorithm_version"`
	Recipient      string     `json:"recipient"`
	Condition      Condition  `json:"condition"`
	Parameters     Parameters `json:"parameters"`
	Funding        *string    `json:"funding_limit_ppm"`
	BeneficiaryCap *string    `json:"per_beneficiary_cap_minor"`
	EventCap       *string    `json:"per_event_total_cap_minor"`
	Frequency      string     `json:"frequency"`
}
type SettlementPolicy struct {
	Period     string  `json:"period"`
	Minimum    string  `json:"minimum_payout_minor"`
	Maximum    *string `json:"max_payment_minor"`
	LatePolicy string  `json:"late_policy"`
	FeeBearer  string  `json:"channel_fee_bearer"`
}
type Config struct {
	PlanID          string           `json:"plan_id"`
	Name            string           `json:"name"`
	Revision        uint64           `json:"revision"`
	StandardVersion string           `json:"standard_version"`
	Route           Route            `json:"route"`
	Scope           Scope            `json:"scope"`
	Currency        string           `json:"currency"`
	CalendarVersion string           `json:"calendar_version"`
	AgentPolicy     Policy           `json:"agent_policy"`
	Grades          []Grade          `json:"grade_definitions"`
	Rules           []Rule           `json:"rules"`
	EventBudget     *string          `json:"event_budget_minor"`
	Settlement      SettlementPolicy `json:"settlement_policy"`
}

// MarshalJSON 保留顶层空AND的children=[]，不能因省略空字段改变配置语义。
func (c Condition) MarshalJSON() ([]byte, error) {
	if c.Logic != "" {
		children := c.Children
		if children == nil {
			children = []Condition{}
		}
		return json.Marshal(struct {
			Logic    string      `json:"logic"`
			Children []Condition `json:"children"`
		}{c.Logic, children})
	}
	return json.Marshal(struct {
		Metric   string          `json:"metric"`
		Operator string          `json:"operator"`
		Value    json.RawMessage `json:"value"`
		Unit     string          `json:"unit"`
	}{c.Metric, c.Operator, c.Value, c.Unit})
}
