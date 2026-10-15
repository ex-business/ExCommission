package application

import (
	"context"
	"fmt"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	result "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"github.com/ex-business/ExCommission/internal/users/ports"
	"time"
)

type SourceKey struct{ Source, Version string }
type SyncRequest struct {
	Source         string  `json:"source_id"`
	MappingVersion string  `json:"mapping_version_id"`
	Cursor         *string `json:"cursor"`
}
type Service struct {
	Commands db.Commands
	Repo     ports.Repository
	Query    db.Executor
	Sources  map[SourceKey]ports.Source
	Now      func() time.Time
	ID       func() string
}

func (s Service) Sync(ctx context.Context, p identity.Principal, input SyncRequest, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)

	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	if previous, found, err := s.Commands.Lookup(ctx, scope, "user-sync", key, input); err != nil {
		return foundation.CommandResult{}, err
	} else if found {
		return previous, nil
	}
	source, ok := s.Sources[SourceKey{input.Source, input.MappingVersion}]
	if !ok {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	// 可信来源读取在业务写事务外，随后将本次结果作为事实持久化。
	page, err := source.Fetch(ctx, input.Cursor)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	return s.Commands.Execute(ctx, scope, "user-sync", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		id := s.ID()
		watermark := page.Watermark
		job := ports.Job{ID: id, Status: "SUCCEEDED", Watermark: &watermark, ErrorRefs: []string{}}
		for index, record := range page.Records {
			if !foundation.ValidID(record.ExternalID) || !foundation.ValidID(record.DisplayName) {
				job.Failure++
				job.ErrorRefs = append(job.ErrorRefs, fmt.Sprintf("%s:row:%d:VALIDATION_FAILED", id, index+1))
				continue
			}
			if _, err := s.Repo.Upsert(ctx, tx, scope, input.Source, record, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
			job.Success++
		}
		if job.Failure > 0 {
			job.Status = "PARTIAL_FAILURE"
		}
		if err := s.Repo.SaveJob(ctx, tx, scope, job, input.Source, input.MappingVersion, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return result.Result(202, id, request, job)
	})
}
func (s Service) Get(ctx context.Context, p identity.Principal, id string) (ports.Job, error) {
	return s.Repo.GetJob(ctx, s.Query, foundation.Scope{Tenant: p.Tenant, App: p.App}, id)
}
