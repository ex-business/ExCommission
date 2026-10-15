package runtime

import (
	"context"
	refundrepo "github.com/ex-business/ExCommission/internal/calculation/adapters"
	calculation "github.com/ex-business/ExCommission/internal/calculation/application"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	source "github.com/ex-business/ExCommission/internal/integration/adapters"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"net/http"
	"os"
)

func refundService(store *mysql.Store) calculation.RefundService {
	return calculation.RefundService{Commands: store, UOW: store, Query: store.DB, Repo: refundrepo.Refunds{}, Adjustments: refundrepo.Adjustments{}, Tasks: taskRepository(store), Ledger: ledger.Repository{}, Now: clockUTC, ID: newID}
}
func installRefunds(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	if err := bootstrapFeature(context.Background(), store, tenant, app, "local-operator", "refunds-v1", []string{"refunds:write", "refunds:read"}); err != nil {
		return err
	}
	provider, err := source.NewSource("sandbox", []byte(os.Getenv("LOCAL_SOURCE_KEY")))
	if err != nil {
		return err
	}
	service := refundService(store)
	mux.Handle("POST /v1/refunds", require("refunds:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		input, err := provider.NormalizeRefund(raw, r.Header.Get("X-Source-Signature"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Receive(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/refunds/{id}", require("refunds:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		v, err := service.Repo.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, v.ID, id, v)
		httpapi.Write(w, result)
	}))
	return nil
}
