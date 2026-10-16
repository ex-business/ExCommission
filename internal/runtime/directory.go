package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	repo "github.com/ex-business/ExCommission/internal/directory/adapters"
	app "github.com/ex-business/ExCommission/internal/directory/application"
	model "github.com/ex-business/ExCommission/internal/directory/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func verifySource(raw []byte, signature string) error {
	key := []byte(os.Getenv("LOCAL_SOURCE_KEY"))
	provided, e := hex.DecodeString(signature)
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	if len(key) < 32 || e != nil || !hmac.Equal(mac.Sum(nil), provided) {
		return errors.New("SOURCE_SIGNATURE_INVALID")
	}
	return nil
}
func installDirectory(ctx context.Context, mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, application string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"agents:directory", "agents:sensitive:read", "users:statistics"}}, {"local-approver", []string{"agents:directory"}}} {
		if err := bootstrapFeature(ctx, store, tenant, application, v.subject, "directory-v1", v.permissions); err != nil {
			return err
		}
	}
	service := app.Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Now: clockUTC}
	mux.Handle("POST /v1/member-statistics", require("users:statistics", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		if err = verifySource(raw, r.Header.Get("X-Source-Signature")); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var in model.Statistics
		if err = httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Save(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
	list := func(member bool) auth.AuthorizedHandler {
		return func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			f, err := listFilter(r)
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			currency := r.URL.Query().Get("currency")
			if currency == "" {
				currency = "USD"
			}
			if _, ok := foundation.CurrencyExponents()[currency]; !ok {
				httpapi.Error(w, rid, foundation.ErrValidation)
				return
			}
			at := clockUTC()
			if value := r.URL.Query().Get("as_of"); value != "" {
				at, err = time.Parse(time.RFC3339Nano, value)
				if err != nil || at.After(clockUTC()) {
					httpapi.Error(w, rid, foundation.ErrValidation)
					return
				}
			}
			depth := 0
			if value := r.URL.Query().Get("depth"); value != "" {
				depth, err = strconv.Atoi(value)
				if err != nil || depth < 1 || depth > 3 {
					httpapi.Error(w, rid, foundation.ErrValidation)
					return
				}
			}
			scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
			root := r.PathValue("id")
			if member {
				if p.MemberUserID == "" {
					httpapi.Error(w, rid, principal.ErrPermission)
					return
				}
				err = store.DB.QueryRowContext(r.Context(), `SELECT agent_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND user_id=?`, scope.Tenant, scope.App, p.MemberUserID).Scan(&root)
				if errors.Is(err, sql.ErrNoRows) {
					root = "__no_agent__"
				} else if err != nil {
					httpapi.Error(w, rid, err)
					return
				}
			}
			if !member && root != "" {
				var exists int
				if err = store.DB.QueryRowContext(r.Context(), `SELECT 1 FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=?`, scope.Tenant, scope.App, root).Scan(&exists); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						err = errors.New("NOT_FOUND")
					}
					httpapi.Error(w, rid, err)
					return
				}
			}
			tx, err := store.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			defer tx.Rollback()
			rows, err := (repo.Repository{}).Rows(r.Context(), tx, scope, root, currency, at, depth)
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			summary := model.Aggregate(rows, currency)
			visible := []model.Row{}
			showIP := !member && identity.Authorize(r.Context(), tx, p, p.Tenant, p.App, "agents:sensitive:read") == nil
			for _, v := range rows {
				if v.UserID <= f.After {
					continue
				}
				if v.Statistics != nil && !showIP {
					v.Statistics.LoginIP = nil
				}
				visible = append(visible, v)
				if len(visible) > f.Limit {
					break
				}
			}
			var next *string
			if len(visible) > f.Limit {
				visible = visible[:f.Limit]
				id := visible[len(visible)-1].UserID
				next = &id
			}
			if err = tx.Commit(); err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			result, _ := response.Result(200, "directory", rid, map[string]any{"rows": visible, "summary": summary, "next_after_id": next, "as_of": at.UTC()})
			httpapi.Write(w, result)
		}
	}
	mux.Handle("GET /v1/agent-directory", require("agents:directory", list(false)))
	mux.Handle("GET /v1/agents/{id}/team", require("agents:directory", list(false)))
	mux.Handle("GET /v1/me/team", require("member:read", list(true)))
	return nil
}
