package runtime

import (
	"context"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	adapter "github.com/ex-business/ExCommission/internal/payments/adapters"
	payments "github.com/ex-business/ExCommission/internal/payments/application"
	paymentports "github.com/ex-business/ExCommission/internal/payments/ports"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	statements "github.com/ex-business/ExCommission/internal/settlement/adapters"
	"io"
	"net/http"
	"os"
	"time"
)

func paymentService(store *mysql.Store) payments.Service {
	return payments.Service{Commands: store, UOW: store, Query: store.DB, Repo: adapter.Repository{}, Ledger: ledger.Repository{}, Statements: statements.Repository{}, Providers: map[string]paymentports.Provider{"sandbox": adapter.Sandbox{Key: []byte(os.Getenv("LOCAL_PAYMENT_KEY")), ResultsFile: os.Getenv("LOCAL_PAYMENT_RESULTS_FILE")}}, Now: clockUTC, ID: newID}
}
func installPayments(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	if len(os.Getenv("LOCAL_PAYMENT_KEY")) < 32 {
		return foundation.ErrValidation
	}
	if err := bootstrapFeature(context.Background(), store, tenant, app, "local-operator", "payments-v1", []string{"payments:write", "payments:read", "payments:verify", "payments:reconcile"}); err != nil {
		return err
	}
	service := paymentService(store)
	mux.Handle("POST /v1/payments", require("payments:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input payments.CreateInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Create(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		if result.Status == 202 {
			if err = service.Dispatch(r.Context(), foundation.Scope{Tenant: p.Tenant, App: p.App}, result.ResourceID); err != nil {
				httpapi.Error(w, id, err)
				return
			}
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/payments/{id}", require("payments:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		v, err := service.Repo.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), false)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, v.ID, id, v)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/payments/{id}/verification", require("payments:verify", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct{}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		payload := map[string]string{"payment_id": r.PathValue("id")}
		saved, found, err := store.Lookup(r.Context(), scope, "payment-verify", r.Header.Get("Idempotency-Key"), payload)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		if found {
			httpapi.Write(w, saved)
			return
		}
		if err = service.Verify(r.Context(), scope, r.PathValue("id")); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := store.Execute(r.Context(), scope, "payment-verify", r.Header.Get("Idempotency-Key"), payload, func(tx db.Executor) (foundation.CommandResult, error) {
			v, e := service.Repo.Get(r.Context(), tx, scope, r.PathValue("id"), false)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			return response.Result(200, v.ID, id, v)
		})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.HandleFunc("POST /v1/payment-callbacks/{channel_id}", func(w http.ResponseWriter, r *http.Request) {
		id := newID()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		if err = service.Callback(r.Context(), foundation.Scope{Tenant: tenant, App: app}, r.PathValue("channel_id"), raw, r.Header.Get("X-Payment-Signature")); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, "callback", id, map[string]string{"status": "ACCEPTED"})
		httpapi.Write(w, result)
	})
	mux.Handle("POST /v1/reconciliation-runs", require("payments:reconcile", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			Channel string `json:"channel_id"`
			Range   struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"date_range"`
			Ref string `json:"statement_file_ref"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		from, fromErr := time.Parse(time.RFC3339Nano, input.Range.From)
		to, toErr := time.Parse(time.RFC3339Nano, input.Range.To)
		if fromErr != nil || toErr != nil || !from.Before(to) {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		if !foundation.ValidID(input.Ref) {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		saved, found, err := store.Lookup(r.Context(), scope, "payment-reconcile", r.Header.Get("Idempotency-Key"), input)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		if found {
			httpapi.Write(w, saved)
			return
		}
		if err = service.Reconcile(r.Context(), scope, input.Channel, input.Ref); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := store.Execute(r.Context(), scope, "payment-reconcile", r.Header.Get("Idempotency-Key"), input, func(db.Executor) (foundation.CommandResult, error) {
			return response.Result(200, id, id, map[string]string{"reconciliation_id": id, "status": "COMPLETED"})
		})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
