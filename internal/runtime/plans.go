package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	agentrepo "github.com/ex-business/ExCommission/internal/agents/adapters"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	planrepo "github.com/ex-business/ExCommission/internal/plans/adapters"
	plans "github.com/ex-business/ExCommission/internal/plans/application"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func bootstrapFeature(ctx context.Context, store *mysql.Store, tenant, app, subject, feature string, permissions []string) error {
	return store.Within(ctx, func(tx db.Executor) error {
		return identity.BootstrapOnce(ctx, tx, tenant, app, subject+":"+feature, subject, permissions)
	})
}
func installPlans(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, workflowService *workflow.Service, tenant, app string) error {
	for _, item := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"plans:write", "plans:read", "plans:simulate", "grades:request", "grades:evaluate", "workflow:decide"}}, {"local-approver", []string{"plans:read", "plans:approve", "workflow:decide"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, item.subject, "plans-v1", item.permissions); err != nil {
			return err
		}
	}
	validator, err := planrepo.NewValidator(map[string]int{"USD": 2, "BTC": 8, "POINT": 0}, map[string]bool{"UTC-v1": true})
	if err != nil {
		return err
	}
	service := plans.Service{Commands: store, Contracts: agentrepo.Repository{}, Participants: agentrepo.Repository{}, Query: store.DB, Repo: planrepo.Repository{}, Validator: validator, Simulations: planrepo.Simulations{}, Workflow: workflowrepo.Repository{}, Exponents: validator.Exponents, Now: time.Now, ID: newID}
	grades := plans.GradeService{Service: service, Grades: agentrepo.Repository{}, Subjects: agentrepo.Repository{}, Evaluations: planrepo.Evaluations{}, Statistics: planrepo.SandboxStatisticsFile{Path: os.Getenv("LOCAL_GRADE_STATISTICS_FILE")}}
	publish := func(ctx context.Context, tx db.Executor, s foundation.Scope, r approval.Request, at time.Time) error {
		var input plans.PublicationPayload
		if err := json.Unmarshal(r.Payload, &input); err != nil {
			return err
		}
		return service.Repo.Publish(ctx, tx, s, input.PlanID, input.Revision, input.Hash, newID(), input.EffectiveFrom, at)
	}
	workflowService.Executors["PLAN_PUBLISH"] = publish
	workflowService.Executors["PLAN_ROLLBACK"] = publish
	if workflowService.Rejections == nil {
		workflowService.Rejections = map[string]workflow.Executor{}
	}
	reject := func(ctx context.Context, tx db.Executor, s foundation.Scope, r approval.Request, at time.Time) error {
		var input plans.PublicationPayload
		if err := json.Unmarshal(r.Payload, &input); err != nil {
			return err
		}
		return service.Repo.Reject(ctx, tx, s, input.PlanID, input.Revision, input.Hash, at)
	}
	workflowService.Rejections["PLAN_PUBLISH"] = reject
	workflowService.Rejections["PLAN_ROLLBACK"] = reject
	if workflowService.Failures == nil {
		workflowService.Failures = map[string]workflow.Executor{}
	}
	unlock := func(ctx context.Context, tx db.Executor, s foundation.Scope, r approval.Request, at time.Time) error {
		var input plans.PublicationPayload
		if err := json.Unmarshal(r.Payload, &input); err != nil {
			return err
		}
		return (planrepo.Repository{}).UnlockFailed(ctx, tx, s, input.PlanID, input.Revision, input.Hash)
	}
	workflowService.Failures["PLAN_PUBLISH"] = unlock
	workflowService.Failures["PLAN_ROLLBACK"] = unlock
	workflowService.Executors["GRADE"] = func(ctx context.Context, tx db.Executor, s foundation.Scope, r approval.Request, at time.Time) error {
		var input agent.GradeChange
		if err := json.Unmarshal(r.Payload, &input); err != nil {
			return err
		}
		version, err := service.Repo.AtPlan(ctx, tx, s, input.PlanID, at)
		if err != nil {
			return err
		}
		found := false
		for _, grade := range version.Config.Grades {
			if grade.ID == input.GradeID {
				found = true
			}
		}
		if !found {
			return foundation.ErrValidation
		}
		return (agentrepo.Repository{}).ApplyGrade(ctx, tx, s, input, "MANUAL", r.ID, at)
	}
	raw := func(w http.ResponseWriter, r *http.Request) ([]byte, error) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		return io.ReadAll(r.Body)
	}
	mux.Handle("POST /v1/plans", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		body, err := raw(w, r)
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		result, err := service.Create(r.Context(), p, body, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	for _, path := range []string{"GET /v1/plans/{id}", "GET /v1/plans/{id}/draft"} {
		mux.Handle(path, require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
			data, err := service.Read(r.Context(), p, r.PathValue("id"))
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			result, _ := response.Result(200, r.PathValue("id"), id, data)
			httpapi.Write(w, result)
		}))
	}
	mux.Handle("PUT /v1/plans/{id}/draft", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			Draft    json.RawMessage `json:"draft"`
			Revision uint64          `json:"expected_revision"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Update(r.Context(), p, r.PathValue("id"), input.Draft, input.Revision, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/simulations", require("plans:simulate", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		body, err := raw(w, r)
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		if err = validator.ValidateSimulation(body); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var input plans.SimulationRequest
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Simulate(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/plans/{id}/publication-requests", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input plans.Publication
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.RequestPublication(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/plans/{id}/rollback-requests", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input plans.Rollback
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Rollback(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/plans/{id}/versions", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {

		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
		}
		codec := httpapi.CursorCodec{Key: []byte(os.Getenv("LOCAL_CURSOR_KEY"))}
		cursor := httpapi.Cursor{Tenant: p.Tenant, App: p.App, Root: r.PathValue("id"), At: "versions:v1", Limit: limit}
		if token := r.URL.Query().Get("cursor"); token != "" {
			var err error
			cursor, err = codec.Decode(token, cursor)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
		}
		versions, more, err := service.Repo.VersionPage(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), cursor.Offset, limit)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		var next *string
		if more {
			cursor.Offset += len(versions)
			token, err := codec.Encode(cursor)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			next = &token
		}
		result, _ := response.Result(200, r.PathValue("id"), id, map[string]any{"items": versions, "next_cursor": next})
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/plans/{id}/versions/{version_id}", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		version, err := service.Simulations.ByVersion(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("version_id"))
		if err != nil || version.Config.PlanID != r.PathValue("id") {
			httpapi.Error(w, id, errorsNewNotFound())
			return
		}
		result, _ := response.Result(200, version.ID, id, version)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/grade-change-requests", require("grades:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input agent.GradeChange
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := grades.RequestChange(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/grade-evaluations", require("grades:evaluate", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input plans.EvaluationRequest
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := grades.Evaluate(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
func errorsNewNotFound() error { return errors.New("NOT_FOUND") }
