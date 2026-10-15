package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/plans/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"os"
	"time"
)

// SandboxStatisticsFile 只接受明确标记、带截止时点和水位的本地输入，不以缺来源冒充零。
type SandboxStatisticsFile struct{ Path string }

func (p SandboxStatisticsFile) Freeze(_ context.Context, s foundation.Scope, plan, period string, cutoff time.Time) (ports.Statistics, error) {
	if !s.Valid() || p.Path == "" {
		return ports.Statistics{}, errors.New("UNKNOWN_FACT")
	}
	body, err := os.ReadFile(p.Path)
	if err != nil {
		return ports.Statistics{}, errors.New("UNKNOWN_FACT")
	}
	var statistics ports.Statistics
	if err = json.Unmarshal(body, &statistics); err != nil || statistics.PlanID != plan || statistics.Period != period || !statistics.Cutoff.Equal(cutoff) || statistics.Watermark == "" || statistics.Agents == nil {
		return statistics, errors.New("UNKNOWN_FACT")
	}
	return statistics, nil
}

type Evaluations struct{}

func (Evaluations) Latest(ctx context.Context, tx db.Executor, s foundation.Scope, plan string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT evaluation_id FROM grade_evaluations WHERE tenant_id=? AND app_id=? AND plan_id=? ORDER BY effective_from DESC LIMIT 1`, s.Tenant, s.App, plan).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (Evaluations) Save(ctx context.Context, tx db.Executor, s foundation.Scope, id string, statistics ports.Statistics, results []byte, effective time.Time) error {
	body, err := json.Marshal(statistics)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO grade_evaluations VALUES(?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, id, statistics.PlanID, statistics.Period, statistics.Cutoff.UTC(), statistics.Watermark, body, results, effective.UTC())
	return err
}
