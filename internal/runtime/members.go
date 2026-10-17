package runtime

import (
	"context"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	repo "github.com/ex-business/ExCommission/internal/members/adapters"
	app "github.com/ex-business/ExCommission/internal/members/application"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	sites "github.com/ex-business/ExCommission/internal/sites/adapters"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	"net/http"
	"os"
)

func memberService(store *mysql.Store) app.Service {
	public := os.Getenv("PUBLIC_MEMBER_URL")
	if public == "" {
		public = "http://127.0.0.1:18180/"
	}
	return app.Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Bindings: users.Repository{}, Now: clockUTC, ID: newID, PublicURL: public, Authorize: func(ctx context.Context, tx db.Executor, p principal.Principal) error {
		if err := identity.Authorize(ctx, tx, p, p.Tenant, p.App, "member:refer"); err != nil {
			return err
		}
		return siteAllow(ctx, tx, foundation.Scope{Tenant: p.Tenant, App: p.App})
	}, CanRefer: func(ctx context.Context, tx db.Executor, scope foundation.Scope, agent string) error {
		return (agents.Repository{}).PaymentAllowed(ctx, tx, scope, agent)
	}}
}
func installMemberPortal(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	service := memberService(store)
	mux.Handle("GET /v1/me/referral-link", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		link, err := service.Read(r.Context(), p)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, _ := response.Result(200, link.Code, rid, link)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/me/referral-links", require("member:refer", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in struct{}
		if err := httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Create(r.Context(), p, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/me/referral-binding", require("member:refer", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in struct {
			Code string `json:"code"`
		}
		if err := httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Bind(r.Context(), p, in.Code, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
	for _, kind := range []string{"earnings", "rewards", "agent-rules"} {
		mux.Handle("GET /v1/me/"+kind, require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			if p.MemberUserID == "" {
				httpapi.Error(w, rid, principal.ErrPermission)
				return
			}
			scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
			agent, err := (repo.Repository{}).OwnAgent(r.Context(), store.DB, scope, p.MemberUserID, false)
			if err != nil && err.Error() != "NOT_FOUND" {
				httpapi.Error(w, rid, err)
				return
			}
			data := map[string]any{}
			switch kind {
			case "earnings":
				rows, err := (repo.Repository{}).Earnings(r.Context(), store.DB, scope, agent)
				if err != nil {
					httpapi.Error(w, rid, err)
					return
				}
				data["rows"] = rows
			case "rewards":
				f, err := listFilter(r)
				if err != nil {
					httpapi.Error(w, rid, err)
					return
				}
				rows, err := (repo.Repository{}).Rewards(r.Context(), store.DB, scope, agent, f.After, f.Limit, clockUTC())
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
				data["rows"] = rows
				data["next_after_id"] = next
			case "agent-rules":
				c, err := (sites.Repository{}).Current(r.Context(), store.DB, scope, false)
				if err != nil {
					httpapi.Error(w, rid, err)
					return
				}
				rows, err := (repo.Repository{}).Rules(r.Context(), store.DB, scope, c.PlanID, clockUTC())
				if err != nil {
					httpapi.Error(w, rid, err)
					return
				}
				data["rows"] = rows
				data["agent_enabled"] = c.Enabled
				data["configured"] = c.Configured
				data["is_agent"] = agent != ""
			default:
				httpapi.Error(w, rid, errors.New("NOT_FOUND"))
				return
			}
			result, _ := response.Result(200, "member-"+kind, rid, data)
			httpapi.Write(w, result)
		}))
	}
}
