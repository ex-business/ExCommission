package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	repo "github.com/ex-business/ExCommission/internal/disbursement/adapters"
	app "github.com/ex-business/ExCommission/internal/disbursement/application"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	pay "github.com/ex-business/ExCommission/internal/payments/application"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	wf "github.com/ex-business/ExCommission/internal/workflow/application"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

func disbursementService(store *mysql.Store) app.Service {
	payments := paymentService(store)
	return app.Service{Commands: store, UOW: store, Query: store.DB, Repo: repo.Repository{}, Now: clockUTC, ID: newID, AuthorizeClaim: func(ctx context.Context, tx db.Executor, p principal.Principal) error {
		return identity.Authorize(ctx, tx, p, p.Tenant, p.App, "member:claim")
	}, Beneficiary: func(ctx context.Context, tx db.Executor, scope foundation.Scope, id string) (string, error) {
		var user string
		err := tx.QueryRowContext(ctx, `SELECT a.user_id FROM settlement_statements s JOIN agent_accounts a ON a.tenant_id=s.tenant_id AND a.app_id=s.app_id AND a.agent_id=s.beneficiary_id WHERE s.tenant_id=? AND s.app_id=? AND s.statement_id=?`, scope.Tenant, scope.App, id).Scan(&user)
		return user, err
	}, CreatePayment: func(ctx context.Context, p principal.Principal, i model.Instruction, key string) (foundation.CommandResult, error) {
		return payments.Create(ctx, p, pay.CreateInput{StatementID: i.StatementID, Amount: i.Amount, Channel: i.Channel, Revision: i.StatementRevision}, key, newID())
	}, Validate: func(ctx context.Context, tx db.Executor, s foundation.Scope, i model.Instruction) error {
		provider, ok := payments.Providers[i.Channel]
		if !ok || !provider.Enabled() {
			return foundation.ErrValidation
		}
		v, err := payments.Statements.Get(ctx, tx, s, i.StatementID, false)
		if err != nil {
			return err
		}
		if v.Revision != i.StatementRevision {
			return errors.New("REVISION_CONFLICT")
		}
		if (v.Status != "APPROVED" && v.Status != "PARTIALLY_PAID") || v.Currency != i.Amount.Currency {
			return errors.New("STATE_CONFLICT")
		}
		n, _ := new(big.Int).SetString(i.Amount.Minor, 10)
		min, ok := new(big.Int).SetString(v.Minimum, 10)
		if !ok {
			return errors.New("UNKNOWN_FACT")
		}
		if n.Cmp(min) < 0 {
			return errors.New("PAYMENT_BELOW_MINIMUM")
		}
		if v.Maximum != nil {
			max, ok := new(big.Int).SetString(*v.Maximum, 10)
			if !ok {
				return errors.New("UNKNOWN_FACT")
			}
			if n.Cmp(max) > 0 {
				return errors.New("PAYMENT_LIMIT_EXCEEDED")
			}
		}
		if err = payments.Accounts.PaymentAllowed(ctx, tx, s, v.AgentID); err != nil {
			return err
		}
		available := new(big.Int)
		for _, item := range v.Items {
			e, err := payments.Ledger.Get(ctx, tx, s, item.ID, false)
			if err != nil {
				return err
			}
			owns, err := payments.Statements.Owns(ctx, tx, s, v.ID, item.ID)
			if err != nil {
				return err
			}
			if e.Frozen || e.Pending != "0" || e.Target != item.Target || !owns {
				return errors.New("STALE_APPROVAL")
			}
			value, ok := new(big.Int).SetString(e.Available, 10)
			if !ok {
				return errors.New("UNKNOWN_FACT")
			}
			available.Add(available, value)
		}
		if n.Cmp(available) > 0 {
			return errors.New("INSUFFICIENT_BALANCE")
		}
		return nil
	}}
}

type reviewItem struct {
	ID       string `json:"approval_id"`
	Decision string `json:"decision"`
	Revision uint64 `json:"expected_revision"`
	Hash     string `json:"object_hash"`
	Reason   string `json:"reason"`
	Remark   string `json:"remark,omitempty"`
}
type reviewBatch struct {
	Items []reviewItem `json:"items"`
}
type payoutBatch struct {
	Action string         `json:"action"`
	Items  []model.Action `json:"items"`
}

func batchResults(ctx context.Context, store *mysql.Store, p principal.Principal, kind, key, rid string, payload any, ids []string, op func(string, string) (foundation.CommandResult, error)) (foundation.CommandResult, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !foundation.ValidID(id) || seen[id] {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		seen[id] = true
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	registered, err := store.Execute(ctx, scope, kind, key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		id := newID()
		return response.Result(202, id, rid, map[string]string{"batch_id": id})
	})
	if err != nil || registered.Status >= 400 {
		return registered, err
	}
	results := []map[string]any{}
	for _, id := range ids {
		item, err := op(id, app.StableKey(registered.ResourceID, id))
		if err != nil {
			buffer := &bufferedResponse{header: http.Header{}}
			httpapi.Error(buffer, rid, err)
			item = foundation.CommandResult{Status: buffer.status, Body: buffer.body.Bytes()}
		}
		var body map[string]any
		decoder := json.NewDecoder(bytes.NewReader(item.Body))
		decoder.UseNumber()
		if err = decoder.Decode(&body); err != nil {
			return foundation.CommandResult{}, err
		}
		results = append(results, map[string]any{"resource_id": id, "http_status": item.Status, "response": body})
	}
	return response.Result(200, registered.ResourceID, rid, map[string]any{"batch_id": registered.ResourceID, "results": results})
}
func listFilter(r *http.Request) (repo.Filter, error) {
	q := r.URL.Query()
	f := repo.Filter{Status: q.Get("status"), Agent: q.Get("agent_id"), Currency: q.Get("currency"), Period: q.Get("period"), After: q.Get("after_id"), Statement: q.Get("statement_id"), Limit: 50}
	if q.Get("limit") != "" {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return f, foundation.ErrValidation
		}
		f.Limit = n
	}
	for _, v := range []string{f.Status, f.Agent, f.Currency, f.After, f.Statement} {
		if v != "" && !foundation.ValidID(v) {
			return f, foundation.ErrValidation
		}
	}
	if f.Period != "" {
		if _, e := time.Parse("2006-01-02", f.Period); e != nil {
			return f, foundation.ErrValidation
		}
	}
	return f, nil
}
func installDisbursement(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *wf.Service) {
	service := disbursementService(store)
	mux.Handle("GET /v1/commission-reviews", require("statements:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		f, e := listFilter(r)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		f.Status = r.URL.Query().Get("approval_status")
		if f.Status != "" && f.Status != "PENDING" && f.Status != "APPROVED" && f.Status != "REJECTED" && f.Status != "CANCELLED" && f.Status != "STALE" {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		rows, e := (repo.Repository{}).Reviews(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, f)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		var next *string
		if len(rows) > f.Limit {
			rows = rows[:f.Limit]
			id := rows[len(rows)-1].ID
			next = &id
		}
		result, _ := response.Result(200, "commission-reviews", rid, map[string]any{"rows": rows, "next_after_id": next})
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/commission-review-batches", require("statements:approve", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in reviewBatch
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		ids := []string{}
		items := map[string]reviewItem{}
		for _, i := range in.Items {
			ids = append(ids, i.ID)
			items[i.ID] = i
		}
		result, e := batchResults(r.Context(), store, p, "commission-review-batch", r.Header.Get("Idempotency-Key"), rid, in, ids, func(id, key string) (foundation.CommandResult, error) {
			a, e := approvals.Get(r.Context(), p, id)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			if a.Type != "STATEMENT" {
				return foundation.CommandResult{}, principal.ErrPermission
			}
			i := items[id]
			return approvals.Decide(r.Context(), p, id, wf.Decision{Decision: i.Decision, Revision: i.Revision, ObjectHash: i.Hash, Reason: i.Reason, Remark: i.Remark}, key, rid)
		})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/payout-orders", require("payments:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in model.Prepare
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		result, e := service.Prepare(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("PUT /v1/payout-orders/{id}", require("payments:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in model.Edit
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		result, e := service.Edit(r.Context(), p, r.PathValue("id"), in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/payout-orders/{id}", require("payments:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		i, e := service.Repo.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		i.ClaimStatus, e = commissionClaimStatus(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, i)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		result, _ := response.Result(200, i.ID, rid, i)
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/payout-orders", require("payments:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		f, e := listFilter(r)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		rows, e := (repo.Repository{}).Instructions(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, f)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		var next *string
		if len(rows) > f.Limit {
			rows = rows[:f.Limit]
			id := rows[len(rows)-1].ID
			next = &id
		}
		for j := range rows {
			rows[j].ClaimStatus, e = commissionClaimStatus(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, rows[j])
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
		}
		result, _ := response.Result(200, "payout-orders", rid, map[string]any{"rows": rows, "next_after_id": next})
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/payout-order-batches", require("payments:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in payoutBatch
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if in.Action != "RELEASE" && in.Action != "CANCEL" {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		ids := []string{}
		items := map[string]model.Action{}
		for _, i := range in.Items {
			ids = append(ids, i.ID)
			items[i.ID] = i
		}
		result, e := batchResults(r.Context(), store, p, "payout-batch", r.Header.Get("Idempotency-Key"), rid, in, ids, func(id, key string) (foundation.CommandResult, error) {
			if err := identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "payments:write"); err != nil {
				return foundation.CommandResult{}, err
			}
			return service.Act(r.Context(), p, in.Action, items[id], key, rid)
		})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, result)
	}))
}
