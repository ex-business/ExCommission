package runtime

import (
	"context"
	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	referral "github.com/ex-business/ExCommission/internal/referral/adapters"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"log/slog"
	"time"
)

type maintenanceJob struct {
	name string
	run  func(context.Context) error
}

func maintenanceJobs(store *mysql.Store) []maintenanceJob {
	payments := paymentService(store)
	refunds := refundService(store)
	jobs := []maintenanceJob{{"payment.recovery", func(job context.Context) error { return payments.Recover(job, localScope()) }}, {"refund.recovery", func(job context.Context) error { return refunds.Recover(job, localScope()) }}, {"outbox.delivery", func(job context.Context) error {
		return deliverOutbox(job, store, localScope())
	}}}
	if referralEnabled() {
		service := referralService(store)
		jobs = append(jobs, struct {
			name string
			run  func(context.Context) error
		}{"referral.rewards", func(job context.Context) error {
			if err := service.Drain(job, localScope()); err != nil {
				return err
			}
			return service.RefreshAll(job, localScope())
		}})
	}
	if affiliateFlowEnabled() {
		jobs = append(jobs, maintenanceJob{"affiliate.business.daily", func(ctx context.Context) error {
			return runAffiliateTaskMaintenance(ctx, store)
		}})
	}
	jobs = append(jobs, maintenanceJob{"payout.instructions", func(ctx context.Context) error { return disbursementService(store).Recover(ctx, localScope()) }})
	jobs = append(jobs, maintenanceJob{"settlement.daily", func(ctx context.Context) error {
		_, err := scheduledSettlement(store).RunOnce(ctx, localScope())
		return err
	}})
	return jobs
}
func startMaintenance(ctx context.Context, store *mysql.Store) {
	for _, job := range maintenanceJobs(store) {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					jobCtx, cancel := context.WithTimeout(foundation.WithOperation(foundation.WithScope(foundation.WithRequest(ctx, newID(), "system-worker"), localScope()), job.name, false), 20*time.Second)
					err := job.run(jobCtx)
					cancel()
					if err != nil && ctx.Err() == nil {
						slog.Error("后台处理失败，保留原记录等待恢复", "operation", job.name)
					}
				}
			}
		}()
	}
}

func deliverOutbox(ctx context.Context, store *mysql.Store, scope foundation.Scope) error {
	return store.DeliverOutbox(ctx, scope, func(event mysql.OutboxEvent) error {
		if referralEnabled() && event.Topic == "member.qualification.updated.v1" {
			if err := (referral.Repository{}).Enqueue(ctx, store.DB, event.Scope, model.Event{ID: event.ID, Body: event.Payload, At: event.CreatedAt}); err != nil {
				return err
			}
		}
		if err := audit.Deliver(ctx, store.DB, event.Scope, event.ID, event.Topic, event.Payload, event.CreatedAt); err != nil {
			return err
		}
		return (catalog.Repository{}).Send(ctx, store.DB, event.Scope, event.ID, event.Topic, event.Payload, event.CreatedAt)
	})
}
