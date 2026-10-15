package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Statistics struct {
	PlanID    string                       `json:"plan_id"`
	Period    string                       `json:"calendar_period"`
	Cutoff    time.Time                    `json:"cutoff"`
	Watermark string                       `json:"source_watermark"`
	Agents    map[string]map[string]string `json:"agents"`
}
type StatisticsProvider interface {
	Freeze(context.Context, foundation.Scope, string, string, time.Time) (Statistics, error)
}
type Evaluations interface {
	Latest(context.Context, db.Executor, foundation.Scope, string) (string, error)
	Save(context.Context, db.Executor, foundation.Scope, string, Statistics, []byte, time.Time) error
}
