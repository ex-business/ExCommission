package application

import (
	"context"
	"encoding/json"
	"errors"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	"time"
)

type Rollback struct {
	VersionID string    `json:"target_version_id"`
	Effective time.Time `json:"effective_from"`
	Reason    string    `json:"reason"`
	Revision  uint64    `json:"expected_revision"`
}

func (s Service) Rollback(ctx context.Context, p identity.Principal, id string, input Rollback, key, request string) (foundation.CommandResult, error) {
	if input.VersionID == "" || input.Reason == "" || input.Effective.IsZero() {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	payload := struct {
		PlanID   string   `json:"plan_id"`
		Rollback Rollback `json:"rollback"`
	}{id, input}
	return s.Commands.Execute(ctx, scope, "plan-rollback", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		current, err := s.Repo.Read(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if current.Revision != input.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		target, err := s.Simulations.ByVersion(ctx, tx, scope, input.VersionID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if target.Config.PlanID != id {
			return foundation.CommandResult{}, errors.New("NOT_FOUND")
		}
		sourceProof, err := s.Simulations.LatestPassed(ctx, tx, scope, target.Hash)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		config := target.Config
		config.Revision = current.Revision + 1
		body, err := json.Marshal(config)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		config, canonical, err := s.Validator.Decode(body)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		var snapshot calculation.Input
		if err = json.Unmarshal(sourceProof.Snapshot, &snapshot); err != nil {
			return foundation.CommandResult{}, err
		}
		snapshot.Plan = config
		snapshot.SnapshotID = s.ID()
		snapshot.TaskID = "simulation-rollback"
		result := calculation.Calculate(snapshot)
		if result.Status != "SUCCEEDED" && result.Status != "NO_MATCH" {
			return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
		}
		if input.Effective.Before(s.Now()) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		configHash := hash(canonical)
		if err = s.Repo.Update(ctx, tx, scope, config, canonical, configHash, current.Revision, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		snapshotBody, err := json.Marshal(snapshot)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		snapshotBody, err = foundation.CanonicalJSON(snapshotBody)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		resultBody, err := json.Marshal(result)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		proofID := s.ID()
		if err = s.Simulations.Save(ctx, tx, scope, ports.Simulation{ID: proofID, PlanID: id, ConfigHash: configHash, SnapshotHash: hash(snapshotBody), Snapshot: snapshotBody, Result: resultBody, Passed: true, At: s.Now()}); err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.SetPending(ctx, tx, scope, id, config.Revision, configHash); err != nil {
			return foundation.CommandResult{}, err
		}
		approvalPayload, err := json.Marshal(PublicationPayload{PlanID: id, Revision: config.Revision, Hash: configHash, EffectiveFrom: input.Effective.UTC()})
		if err != nil {
			return foundation.CommandResult{}, err
		}
		approvalPayload, err = foundation.CanonicalJSON(approvalPayload)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		approvalID := s.ID()
		if err = s.Workflow.Create(ctx, tx, scope, approval.Request{ID: approvalID, Type: "PLAN_ROLLBACK", Applicant: p.Subject, ObjectRef: id, ObjectHash: hash(approvalPayload), Payload: approvalPayload}, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, approvalID, request, map[string]any{"approval_id": approvalID, "simulation_id": proofID, "draft_hash": configHash})
	})
}
