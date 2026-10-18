// Package affiliatetasks 记录代理日结的真实依赖与执行历史；任务管理不持有修改佣金结果的权限。
package affiliatetasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"time"
)

const (
	Ready         = "READY"
	WaitingTime   = "WAITING_TIME"
	WaitingSource = "WAITING_WATERMARK"
	Running       = "RUNNING"
	Failed        = "FAILED"
	Succeeded     = "SUCCEEDED"
	Blocked       = "BLOCKED"
)

type Key struct {
	Program  string    `json:"program_id"`
	Revision uint64    `json:"program_revision"`
	Day      string    `json:"business_day"`
	Timezone string    `json:"timezone"`
	Due      time.Time `json:"due_at"`
}

func (k Key) ID() string {
	raw, _ := json.Marshal([]any{"affiliate-day-task", k.Program, k.Revision, k.Day})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (k Key) Validate() error {
	if !foundation.ValidID(k.Program) || len(k.Program) > 64 || k.Due.IsZero() {
		return foundation.ErrValidation
	}
	if _, e := time.Parse("2006-01-02", k.Day); e != nil {
		return foundation.ErrValidation
	}
	if _, e := time.LoadLocation(k.Timezone); e != nil {
		return foundation.ErrValidation
	}
	return nil
}

type Missing struct {
	Source string `json:"source_id"`
	Day    string `json:"business_day"`
}
type Report struct {
	Status    string    `json:"status"`
	Phase     string    `json:"phase"`
	Reason    string    `json:"reason"`
	Missing   []Missing `json:"missing_watermarks"`
	Completed int       `json:"completed_units"`
	Total     int       `json:"total_units"`
}
type Record struct {
	Key
	ID string `json:"task_id"`
	Report
	AutoAttempts   int        `json:"auto_attempts"`
	Attempts       int        `json:"attempts"`
	Failures       int        `json:"consecutive_failures"`
	Created        time.Time  `json:"created_at"`
	Updated        time.Time  `json:"updated_at"`
	Checked        time.Time  `json:"checked_at"`
	WaitingSince   *time.Time `json:"waiting_since,omitempty"`
	Started        *time.Time `json:"started_at,omitempty"`
	Finished       *time.Time `json:"finished_at,omitempty"`
	NextRetry      *time.Time `json:"next_retry_at,omitempty"`
	RequestID      string     `json:"request_id,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	RetryRequested bool       `json:"retry_requested"`
	RequestedBy    string     `json:"retry_requested_by,omitempty"`
	RetryReason    string     `json:"retry_reason,omitempty"`
	RequestedAt    *time.Time `json:"retry_requested_at,omitempty"`
	LeaseToken     string     `json:"-"`
	LeaseUntil     *time.Time `json:"lease_until,omitempty"`
}
type Attempt struct {
	TaskID    string     `json:"task_id"`
	Number    int        `json:"attempt"`
	Status    string     `json:"status"`
	Trigger   string     `json:"trigger"`
	Actor     string     `json:"actor_id"`
	Reason    string     `json:"reason"`
	RequestID string     `json:"request_id"`
	Started   time.Time  `json:"started_at"`
	Finished  *time.Time `json:"finished_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}
type Rules struct {
	Enabled          bool `json:"enabled"`
	WatermarkMinutes int  `json:"watermark_wait_minutes"`
	FailureCount     int  `json:"failure_count"`
	OverdueMinutes   int  `json:"overdue_minutes"`
	AutoRetry        bool `json:"auto_retry"`
	MaxAttempts      int  `json:"max_auto_attempts"`
	BaseSeconds      int  `json:"retry_base_seconds"`
	MaxSeconds       int  `json:"retry_max_seconds"`
}

func DefaultRules() Rules { return Rules{true, 30, 3, 60, true, 5, 30, 900} }
func (r Rules) Validate() error {
	if r.WatermarkMinutes < 1 || r.WatermarkMinutes > 10080 || r.FailureCount < 1 || r.FailureCount > 100 || r.OverdueMinutes < 1 || r.OverdueMinutes > 43200 || r.MaxAttempts < 1 || r.MaxAttempts > 100 || r.BaseSeconds < 5 || r.BaseSeconds > 3600 || r.MaxSeconds < r.BaseSeconds || r.MaxSeconds > 86400 {
		return foundation.ErrValidation
	}
	return nil
}

type RuleVersion struct {
	Revision   uint64    `json:"revision"`
	Rules      Rules     `json:"rules"`
	ApprovalID string    `json:"approval_id"`
	Published  time.Time `json:"published_at"`
}
type RuleChange struct {
	Revision uint64 `json:"expected_revision"`
	Rules    Rules  `json:"rules"`
	Reason   string `json:"reason"`
}
type Alert struct {
	ID           string     `json:"alert_id"`
	TaskID       string     `json:"task_id"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	Reason       string     `json:"reason"`
	Triggered    time.Time  `json:"triggered_at"`
	Resolved     *time.Time `json:"resolved_at,omitempty"`
	RuleRevision uint64     `json:"rule_revision"`
}

var ErrMissing = errors.New("NOT_FOUND")
var ErrBusy = errors.New("STATE_CONFLICT:TASK_BUSY")

type Inspector func(context.Context) (Report, error)
type Executor func(context.Context) error
type Run func(context.Context, foundation.Scope, Key, Inspector, Executor) error
type progressKey struct{}
type progressFn func(string, int, int)

// Progress 只记录处理阶段，事务提交前不会标记成功；进度失败也不能使资金事务误判已完成。
func Progress(ctx context.Context, phase string, done, total int) {
	if fn, ok := ctx.Value(progressKey{}).(progressFn); ok {
		fn(phase, done, total)
	}
}
func DueAt(date time.Time) time.Time {
	next := date.AddDate(0, 0, 1)
	return time.Date(next.Year(), next.Month(), next.Day(), 1, 0, 0, 0, next.Location()).UTC()
}
