package runtime

import (
	"context"
	walletsandbox "github.com/ex-business/ExCommission/internal/payments/adapters/walletsandbox"

	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	adapter "github.com/ex-business/ExCommission/internal/payments/adapters"
	payments "github.com/ex-business/ExCommission/internal/payments/application"
	paymentmodel "github.com/ex-business/ExCommission/internal/payments/domain"
	paymentports "github.com/ex-business/ExCommission/internal/payments/ports"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	statements "github.com/ex-business/ExCommission/internal/settlement/adapters"
	walletrepo "github.com/ex-business/ExCommission/internal/wallet/adapters"
	wallet "github.com/ex-business/ExCommission/internal/wallet/application"
	walletmodel "github.com/ex-business/ExCommission/internal/wallet/domain"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func paymentService(store *mysql.Store) payments.Service {
	service := payments.Service{BeforeSubmit: siteAllow, Commands: store, UOW: store, Query: store.DB, Repo: adapter.Repository{}, Ledger: ledger.Repository{}, Accounts: sitePaymentAccounts{}, Statements: statements.Repository{}, Credit: func(ctx context.Context, tx db.Executor, scope foundation.Scope, p paymentmodel.Payment) error {
		var user string
		if err := tx.QueryRowContext(ctx, `SELECT a.user_id FROM settlement_statements s JOIN agent_accounts a ON a.tenant_id=s.tenant_id AND a.app_id=s.app_id AND a.agent_id=s.beneficiary_id WHERE s.tenant_id=? AND s.app_id=? AND s.statement_id=?`, scope.Tenant, scope.App, p.StatementID).Scan(&user); err != nil {
			return err
		}
		return (wallet.Service{Repo: walletrepo.Repository{}}).Credit(ctx, tx, scope, walletmodel.Credit{UserID: user, Currency: p.Currency, Amount: p.Amount, Reference: "wallet-" + p.MerchantID, PaymentID: p.ID, StatementID: p.StatementID}, clockUTC())
	}, Providers: map[string]paymentports.Provider{"wallet": walletrepo.Provider{}, "sandbox": configuredPaymentProvider{Provider: adapter.Sandbox{Key: []byte(os.Getenv("LOCAL_PAYMENT_KEY")), ResultsFile: os.Getenv("LOCAL_PAYMENT_RESULTS_FILE")}, Store: store, Scope: localScope()}}, Now: clockUTC, ID: newID}
	if path := os.Getenv("WALLET_SANDBOX_CONFIG_FILE"); path != "" {
		if config, err := walletsandbox.Load(path); err == nil {
			service.Providers["wallet-sandbox"] = walletsandbox.Provider{Config: config}
		}
	}
	return service
}
func installPayments(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	if len(os.Getenv("LOCAL_PAYMENT_KEY")) < 32 {
		return foundation.ErrValidation
	}
	if err := bootstrapFeature(context.Background(), store, tenant, app, "local-operator", "payments-v1", []string{"payments:write", "payments:read", "payments:verify", "payments:reconcile"}); err != nil {
		return err
	}
	if path := os.Getenv("WALLET_SANDBOX_CONFIG_FILE"); path != "" {
		if _, err := walletsandbox.Load(path); err != nil {
			return err
		}
	}
	service := paymentService(store)
	// 专用回执入口通过服务端客户端映射选择站点；原回执入口不承担多站钱包接入。
	if provider, ok := service.Providers["wallet-sandbox"].(walletsandbox.Provider); ok {
		mux.HandleFunc("POST /v1/wallet-sandbox-callbacks/{client_id}", func(w http.ResponseWriter, r *http.Request) {
			requestID := newID()
			bound, ok := provider.ByClient(r.PathValue("client_id"))
			if !ok {
				httpapi.Error(w, requestID, foundation.ErrValidation)
				return
			}
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				httpapi.Error(w, requestID, foundation.ErrValidation)
				return
			}
			outcome, err := bound.VerifyCallback(raw, r.Header.Get("X-Payment-Signature"))
			if err == nil {
				err = service.Apply(r.Context(), bound.Scope, "wallet-sandbox", outcome)
			}
			if err != nil {
				httpapi.Error(w, requestID, err)
				return
			}
			slog.Info("独立钱包回执已处理", "request_id", requestID, "tenant_id", bound.Scope.Tenant, "app_id", bound.Scope.App, "callback_id", outcome.CallbackID, "merchant_payment_id", outcome.MerchantID, "status", outcome.Status)
			w.WriteHeader(http.StatusNoContent)
		})
	}
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
	mux.Handle("POST /v1/payment-callbacks/{channel_id}", auditHTTP(store, nil, foundation.Scope{Tenant: tenant, App: app}, "payment.callback", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))
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
		reconcileService := service
		if input.Channel == "wallet-sandbox" {
			if provider, ok := service.Providers[input.Channel].(walletsandbox.Provider); ok {
				reconcileService.Providers = map[string]paymentports.Provider{}
				for key, value := range service.Providers {
					reconcileService.Providers[key] = value
				}
				reconcileService.Providers[input.Channel] = provider.ForScope(scope)
			}
		}
		if err = reconcileService.Reconcile(r.Context(), scope, input.Channel, input.Ref); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := store.Execute(r.Context(), scope, "payment-reconcile", r.Header.Get("Idempotency-Key"), input, func(db.Executor) (foundation.CommandResult, error) {
			return response.Result(200, id, id, map[string]string{"run_id": id, "reconciliation_id": id, "status": "COMPLETED"})
		})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}

type configuredPaymentProvider struct {
	paymentports.Provider
	Store *mysql.Store
	Scope foundation.Scope
}

func (p configuredPaymentProvider) Enabled() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	enabled, _, err := (catalog.Repository{}).Component(ctx, p.Store.DB, p.Scope, "PAYMENT", "sandbox")
	return err == nil && enabled && p.Provider.Enabled()
}
