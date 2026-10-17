package domain

import (
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"time"
)

// InitialPeriod avoids an unbounded historical sweep on the first deployment.
func InitialPeriod(now time.Time) string { return now.UTC().AddDate(0, 0, -1).Format("2006-01-02") }
func DueAt(period string) (time.Time, error) {
	day, err := time.Parse("2006-01-02", period)
	if err != nil || day.Format("2006-01-02") != period {
		return time.Time{}, foundation.ErrValidation
	}
	return day.AddDate(0, 0, 1).Add(time.Hour), nil
}
func NextPeriod(period string) (string, error) {
	day, err := time.Parse("2006-01-02", period)
	if err != nil {
		return "", foundation.ErrValidation
	}
	return day.AddDate(0, 0, 1).Format("2006-01-02"), nil
}

type ScheduledResult struct {
	Period       string    `json:"period"`
	RunID        string    `json:"run_id"`
	StatementIDs []string  `json:"statement_ids"`
	ApprovalIDs  []string  `json:"approval_ids"`
	Completed    time.Time `json:"completed_at"`
}
type Schedule struct {
	NextPeriod string           `json:"next_period"`
	Last       *ScheduledResult `json:"last_result"`
}
