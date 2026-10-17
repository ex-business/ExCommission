package runtime

import (
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	repo "github.com/ex-business/ExCommission/internal/wallet/adapters"
	"net/http"
)

func installWallet(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	mux.Handle("GET /v1/me/wallets", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		if p.MemberUserID == "" {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		rows, err := (repo.Repository{}).Accounts(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, p.MemberUserID)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, _ := response.Result(200, "wallets", rid, map[string]any{"rows": rows})
		httpapi.Write(w, result)
	}))
	for _, kind := range []string{"wallet-transactions", "wallet-changes"} {
		mux.Handle("GET /v1/me/"+kind, require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			if p.MemberUserID == "" {
				httpapi.Error(w, rid, principal.ErrPermission)
				return
			}
			f, err := listFilter(r)
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			rows, err := (repo.Repository{}).Entries(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, p.MemberUserID, f.Currency, f.After, f.Limit, kind == "wallet-changes")
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			var next *string
			if len(rows) > f.Limit {
				rows = rows[:f.Limit]
				id := rows[len(rows)-1].ID
				next = &id
			}
			result, _ := response.Result(200, kind, rid, map[string]any{"rows": rows, "next_after_id": next})
			httpapi.Write(w, result)
		}))
	}
}
