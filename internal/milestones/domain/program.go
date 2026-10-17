package domain

import (
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"time"
)

type Condition struct {
	Metric    string `json:"metric"`
	Unit      string `json:"unit"`
	Threshold string `json:"threshold"`
}
type Program struct {
	ID         string      `json:"program_id"`
	Name       string      `json:"name"`
	Source     string      `json:"source_id"`
	Starts     time.Time   `json:"starts_at"`
	Ends       time.Time   `json:"ends_at"`
	Conditions []Condition `json:"conditions"`
	Logic      string      `json:"logic"`
	Frequency  string      `json:"frequency"`
	PlanID     string      `json:"reward_plan_id"`
}
type Observation struct {
	Source       string    `json:"source_id"`
	EventID      string    `json:"event_id"`
	ProgramID    string    `json:"program_id"`
	ExternalUser string    `json:"external_user_id"`
	Beneficiary  string    `json:"beneficiary_agent_id"`
	Metric       string    `json:"metric"`
	Unit         string    `json:"unit"`
	Value        string    `json:"cumulative_value"`
	At           time.Time `json:"observed_at"`
	Evidence     string    `json:"evidence_ref"`
}
type Fact struct {
	Value    string    `json:"cumulative_value"`
	At       time.Time `json:"observed_at"`
	Evidence string    `json:"evidence_ref"`
}
type Progress struct {
	UserID          string          `json:"user_id"`
	Beneficiary     string          `json:"beneficiary_agent_id"`
	BoundAt         time.Time       `json:"bound_at"`
	BindingEvidence string          `json:"binding_evidence_ref"`
	Facts           map[string]Fact `json:"facts"`
	TaskID          *string         `json:"task_id"`
	QualifiedAt     *time.Time      `json:"qualified_at"`
}

func UTC(t time.Time) bool {
	return !t.IsZero() && t.Location() != nil && t.Format("-07:00") == "+00:00" && t.Equal(t.Truncate(time.Microsecond))
}
func (p *Program) Normalize() error {
	if !foundation.ValidID(p.ID) || !foundation.ValidID(p.Name) || !foundation.ValidID(p.Source) || !foundation.ValidID(p.PlanID) || p.Logic != "ALL" || p.Frequency != "ONCE_PER_SUBJECT" || !UTC(p.Starts) || !UTC(p.Ends) || !p.Starts.Before(p.Ends) || len(p.Conditions) < 1 || len(p.Conditions) > 16 {
		return foundation.ErrValidation
	}
	seen := map[string]bool{}
	for i, c := range p.Conditions {
		d, e := foundation.ParseDecimal(c.Threshold)
		if e != nil || d.Atoms().Sign() <= 0 || !foundation.ValidID(c.Metric) || !foundation.ValidID(c.Unit) || seen[c.Metric] {
			return foundation.ErrValidation
		}
		seen[c.Metric] = true
		p.Conditions[i].Threshold = d.String()
	}
	p.Starts = p.Starts.UTC()
	p.Ends = p.Ends.UTC()
	return nil
}
func (o *Observation) Normalize() error {
	for _, v := range []string{o.Source, o.EventID, o.ProgramID, o.ExternalUser, o.Beneficiary, o.Metric, o.Unit, o.Evidence} {
		if !foundation.ValidID(v) {
			return foundation.ErrValidation
		}
	}
	d, e := foundation.ParseDecimal(o.Value)
	if e != nil || !UTC(o.At) {
		return foundation.ErrValidation
	}
	o.Value = d.String()
	o.At = o.At.UTC()
	return nil
}
func (p Program) Condition(metric string) (Condition, bool) {
	for _, c := range p.Conditions {
		if c.Metric == metric {
			return c, true
		}
	}
	return Condition{}, false
}

// Reached requires known facts for every configured condition, with exact fixed-point comparisons.
func (p Program) Reached(facts map[string]Fact) (bool, time.Time) {
	at := p.Starts
	for _, c := range p.Conditions {
		f, ok := facts[c.Metric]
		if !ok {
			return false, time.Time{}
		}
		value, e := foundation.ParseDecimal(f.Value)
		target, e2 := foundation.ParseDecimal(c.Threshold)
		if e != nil || e2 != nil || value.Atoms().Cmp(target.Atoms()) < 0 {
			return false, time.Time{}
		}
		if f.At.After(at) {
			at = f.At
		}
	}
	return true, at
}
