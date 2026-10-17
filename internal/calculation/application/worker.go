package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/calculation/domain"
	"github.com/ex-business/ExCommission/internal/calculation/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Worker struct {
	BeforeCommit func(context.Context, db.Executor, foundation.Scope, domain.Task, domain.Input) error
	UOW          db.UnitOfWork
	Tasks        ports.Tasks
	Resolver     ports.SnapshotResolver
	Committer    ports.Committer
	TargetPolicy ports.TargetPolicy
	Now          func() time.Time
	Lease        time.Duration
}

func (w Worker) RunOnce(ctx context.Context, s foundation.Scope) (bool, error) {
	task, found, err := w.Tasks.Claim(ctx, s, w.Now(), w.Lease)
	if err != nil || !found {
		return found, err
	}
	var snapshot domain.Input
	if len(task.Snapshot) == 0 {
		err = w.UOW.Within(ctx, func(tx db.Executor) error {
			var err error
			snapshot, err = w.Resolver.Resolve(ctx, tx, s, task)
			if err != nil {
				return err
			}
			body, err := json.Marshal(snapshot)
			if err != nil {
				return err
			}
			body, err = foundation.CanonicalJSON(body)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			if err = w.Tasks.SaveSnapshot(ctx, tx, s, task, body, hex.EncodeToString(sum[:]), w.Now()); err != nil {
				return err
			}
			task.Snapshot = body
			hash := hex.EncodeToString(sum[:])
			task.SnapshotHash = &hash
			return nil
		})
	} else {
		sum := sha256.Sum256(task.Snapshot)
		if task.SnapshotHash == nil || *task.SnapshotHash != hex.EncodeToString(sum[:]) {
			err = errors.New("SNAPSHOT_INTEGRITY_FAILED")
		} else {
			err = json.Unmarshal(task.Snapshot, &snapshot)
		}
	}
	if err != nil {
		return true, w.failure(ctx, s, task, err)
	}
	result := domain.Calculate(snapshot)
	body, err := json.Marshal(result)
	if err != nil {
		return true, w.failure(ctx, s, task, err)
	}
	if result.Status == "SUSPENDED" {
		reason := "UNKNOWN_FACT"
		if len(result.Reasons) > 0 {
			reason = result.Reasons[0]
		}
		return true, w.UOW.Within(ctx, func(tx db.Executor) error {
			return w.Tasks.Finish(ctx, tx, s, task, "SUSPENDED", &reason, body, w.Now())
		})
	}
	if w.Committer == nil {
		return true, w.failure(ctx, s, task, errors.New("LEDGER_NOT_READY"))
	}
	err = w.UOW.Within(ctx, func(tx db.Executor) error {
		if w.BeforeCommit != nil {
			if err := w.BeforeCommit(ctx, tx, s, task, snapshot); err != nil {
				return err
			}
		}
		if w.TargetPolicy != nil {
			var err error
			result, err = w.TargetPolicy.Prepare(ctx, tx, s, task, snapshot, result)
			if err != nil {
				return err
			}
			body, err = json.Marshal(result)
			if err != nil {
				return err
			}
		}
		if err := w.Committer.Commit(ctx, tx, s, task, snapshot, result, w.Now()); err != nil {
			return err
		}
		return w.Tasks.Finish(ctx, tx, s, task, result.Status, nil, body, w.Now())
	})
	if err != nil {
		return true, w.failure(ctx, s, task, err)
	}
	return true, nil
}
func (w Worker) failure(ctx context.Context, s foundation.Scope, t domain.Task, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	if ctx.Err() != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ctx = recoveryCtx
	}
	code := cause.Error()
	switch code {
	case "REVISION_CONFLICT":
		return cause
	case "DAYEND_PENDING":
		// 日终扩展负责等待与冻结互斥，避免worker先读缺失、冻结后才挂起导致漏唤醒。
		if pending, ok := w.Resolver.(interface {
			WaitPending(context.Context, db.Executor, foundation.Scope, domain.Task, time.Time) error
		}); ok {
			return w.UOW.Within(ctx, func(tx db.Executor) error { return pending.WaitPending(ctx, tx, s, t, w.Now()) })
		}
		return w.UOW.Within(ctx, func(tx db.Executor) error { return w.Tasks.Finish(ctx, tx, s, t, "SUSPENDED", &code, nil, w.Now()) })
	case "SITE_AGENT_DISABLED", "VENUE_NOT_PARTICIPATING", "DAYEND_LATE_FACT", "NO_EFFECTIVE_PLAN_VERSION", "RELATION_HISTORY_MISSING", "UNKNOWN_FACT", "RATE_MISSING", "UNIT_MISMATCH", "VALIDATION_FAILED", "BASIS_CURRENCY_MISMATCH", "BUDGET_EXCEEDED", "SNAPSHOT_INTEGRITY_FAILED", "LEDGER_NOT_READY", "UNSUPPORTED_CAPABILITY", "NOT_FOUND", "REFUND_QUANTITY_MISSING", "REFUND_EXCEEDS_ORIGINAL", "REFUND_TARGET_INCREASE":
		return w.UOW.Within(ctx, func(tx db.Executor) error { return w.Tasks.Finish(ctx, tx, s, t, "SUSPENDED", &code, nil, w.Now()) })
	default:
		return w.Tasks.Retry(ctx, s, t, w.Now())
	}
}
