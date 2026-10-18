package runtime

import (
	"encoding/json"
	"errors"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	"github.com/ex-business/ExCommission/internal/affiliateflow/ports"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"net/http"
)

func installAffiliateReversals(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	service := affiliateFlowService(store)
	mux.Handle("POST /v1/affiliate/reversals", require("affiliate:ingest", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
		if e != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		out, e := service.ReceiveReversal(r.Context(), p, raw, r.Header.Get("X-Affiliate-Signature"), r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			if errors.Is(e, flow.ErrReversalPaymentPending) || errors.Is(e, flow.ErrReversalOriginalPending) { // 仅此独立接口把在途资金标记为可重试，来源不得换单号或认定退款已完成。
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				code := "PAYMENT_RESULT_UNKNOWN"
				if errors.Is(e, flow.ErrReversalOriginalPending) {
					code = "ORIGINAL_EVENT_PENDING"
				}
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code, "retryable": true, "resource_ref": nil, "details": map[string]string{}}, "request_id": rid})
				return
			}
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/affiliate/reversals", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		repo, ok := service.Repo.(ports.Reversals)
		if !ok {
			httpapi.Error(w, rid, errors.New("UNSUPPORTED_CAPABILITY"))
			return
		}
		items, e := repo.ReversalRecords(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		events, e := repo.ReversalEvents(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := response.Result(200, "affiliate-reversals", rid, map[string]any{"items": items, "events": events, "limit": 1000})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
}
