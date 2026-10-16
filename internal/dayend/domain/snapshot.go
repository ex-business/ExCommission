package domain

import (
	"errors"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"time"
)

type Agent struct {
	Grade   string            `json:"grade_id"`
	Metrics map[string]string `json:"metrics"`
}
type Snapshot struct {
	Watermark string            `json:"source_watermark"`
	PlanID    string            `json:"plan_id"`
	VersionID string            `json:"plan_version_id"`
	Day       string            `json:"business_day"`
	Start     time.Time         `json:"start"`
	End       time.Time         `json:"end"`
	Cutoff    time.Time         `json:"cutoff"`
	Policy    plan.DayEndPolicy `json:"policy"`
	TaskIDs   []string          `json:"task_ids"`
	Agents    map[string]Agent  `json:"agents"`
}

// Bounds 使用站点当地自然日，不使用固定24小时，确保夏令时的23/25小时日期正确。
func Bounds(day, timezone string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" || timezone == "Local" {
		return time.Time{}, time.Time{}, errors.New("VALIDATION_FAILED")
	}
	start, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil || start.Format("2006-01-02") != day {
		return time.Time{}, time.Time{}, errors.New("VALIDATION_FAILED")
	}
	return start.UTC(), start.AddDate(0, 0, 1).UTC(), nil
}
func Includes(depths []int, depth int) bool {
	for _, d := range depths {
		if d == depth {
			return true
		}
	}
	return false
}
