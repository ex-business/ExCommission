package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	events "github.com/ex-business/ExCommission/internal/integration/adapters"
	event "github.com/ex-business/ExCommission/internal/integration/domain"
	repository "github.com/ex-business/ExCommission/internal/milestones/adapters"
	milestones "github.com/ex-business/ExCommission/internal/milestones/application"
	model "github.com/ex-business/ExCommission/internal/milestones/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"io"
	"net/http"
	"os"
	"time"
)

func milestoneService(store *mysql.Store) milestones.Service {
	return milestones.Service{Commands: store, Query: store.DB, Repo: repository.Repository{}, Workflow: workflowrepo.Repository{}, Plans: plans.Repository{}, Users: users.Repository{}, Accounts: agents.Repository{}, Now: clockUTC, ID: newID, AllowSource: func(ctx context.Context, tx db.Executor, s foundation.Scope, source string) error {
		if err := siteAllow(ctx, tx, s); err != nil {
			return err
		}
		if source != "sandbox" {
			return errors.New("UNSUPPORTED_CAPABILITY")
		}
		enabled, _, err := (catalog.Repository{}).Component(ctx, tx, s, "SOURCE", source)
		if err != nil {
			return err
		}
		if !enabled {
			return errors.New("UNSUPPORTED_CAPABILITY")
		}
		return nil
	}, Dispatch: func(ctx context.Context, tx db.Executor, s foundation.Scope, program model.Program, p model.Progress, o model.Observation, version plan.Version, at time.Time) (string, error) {
		// Use a reserved event kind rejected by the external ordinary-event schema.
		record, err := (repository.Repository{}).Program(ctx, tx, s, program.ID)
		if err != nil {
			return "", err
		}
		proof, _ := json.Marshal(map[string]any{"program": program, "member": p, "qualified_at": at, "config_hash": record.Hash, "approval_id": record.ApprovalID})
		keybody, _ := json.Marshal([]string{program.ID, p.UserID})
		sum := sha256.Sum256(keybody)
		business := "milestone_" + hex.EncodeToString(sum[:])
		confirmed := true
		e := event.Event{Schema: "1", Source: o.Source, ID: business, BusinessID: business, Type: "MILESTONE_REACHED", Time: at, ExternalUser: o.ExternalUser, Product: version.Config.Route.Product, Scene: version.Config.Route.Scene, Revision: "1", Payload: event.Payload{Basis: "1", Currency: version.Config.Currency, Confirmed: &confirmed, Beneficiary: p.Beneficiary, BindingFrom: &p.BoundAt, BindingEvidence: p.BindingEvidence, TriggerEvidence: proof}}
		body, _ := json.Marshal(e)
		canonical, err := foundation.CanonicalJSON(body)
		if err != nil {
			return "", err
		}
		if _, err = (events.Repository{}).Accept(ctx, tx, s, e, canonical, clockUTC()); err != nil {
			return "", err
		}
		if err = (events.Repository{}).SaveRaw(ctx, tx, s, e, body); err != nil {
			return "", err
		}
		task := calc.Task{ID: newID(), EventID: newID(), Source: e.Source, SourceEvent: e.ID, BusinessID: e.BusinessID, EventType: e.Type, BusinessTime: e.Time, EventBody: canonical, Status: "QUEUED"}
		if err = taskRepository(store).Create(ctx, tx, s, task, clockUTC()); err != nil {
			return "", err
		}
		return task.ID, nil
	}}
}
func installMilestones(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"milestones:request", "milestones:write", "milestones:read"}}, {"local-approver", []string{"milestones:approve", "milestones:read"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "milestones-v1", v.permissions); err != nil {
			return err
		}
	}
	service := milestoneService(store)
	approvals.Executors["MILESTONE_PROGRAM"] = service.Activate
	mux.Handle("POST /v1/milestone-program-requests", require("milestones:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in milestones.Request
		if err := httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Request(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/milestone-observations", require("milestones:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		mac := hmac.New(sha256.New, []byte(os.Getenv("LOCAL_SOURCE_KEY")))
		mac.Write(body)
		sig, e := hex.DecodeString(r.Header.Get("X-Source-Signature"))
		if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
			httpapi.Error(w, rid, errors.New("SOURCE_SIGNATURE_INVALID"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var in model.Observation
		if err = httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Observe(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/milestone-programs/{id}", require("milestones:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		record, err := service.Repo.Program(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, _ := response.Result(200, record.Program.ID, rid, record)
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/milestone-programs/{id}/members/{user_id}", require("milestones:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		record, err := service.Repo.Program(r.Context(), store.DB, scope, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		member, err := service.Repo.Member(r.Context(), store.DB, scope, record.Program.ID, r.PathValue("user_id"))
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		conditions := []map[string]any{}
		for _, c := range record.Program.Conditions {
			fact, known := member.Facts[c.Metric]
			var value *string
			if known {
				v := fact.Value
				value = &v
			}
			conditions = append(conditions, map[string]any{"metric": c.Metric, "unit": c.Unit, "threshold": c.Threshold, "known": known, "cumulative_value": value})
		}
		status := "WAITING"
		var taskStatus *string
		if member.TaskID != nil {
			status = "TRIGGERED"
			task, err := taskRepository(store).Get(r.Context(), store.DB, scope, *member.TaskID)
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			taskStatus = &task.Status
		}
		result, _ := response.Result(200, member.UserID, rid, map[string]any{"program_id": record.Program.ID, "progress": member, "conditions": conditions, "status": status, "task_status": taskStatus})
		httpapi.Write(w, result)
	}))
	return nil
}
