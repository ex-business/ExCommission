package runtime

import (
	"context"
	repo "github.com/ex-business/ExCommission/internal/disbursement/adapters"
	app "github.com/ex-business/ExCommission/internal/disbursement/application"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"net/http"
)

func commissionClaimStatus(ctx context.Context, tx db.Executor, s foundation.Scope, i model.Instruction) (string, error) {
	if i.DeliveryMode != "MEMBER_CLAIM" {
		return "", nil
	}
	status, recon := "", ""
	if i.PaymentID != nil {
		err := tx.QueryRowContext(ctx, `SELECT status,reconciliation_status FROM payment_orders WHERE tenant_id=? AND app_id=? AND payment_id=?`, s.Tenant, s.App, *i.PaymentID).Scan(&status, &recon)
		if err != nil {
			return "", err
		}
	}
	return model.ClaimState(i.Status, status, recon), nil
}
func installMemberClaims(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	service := disbursementService(store)
	mux.Handle("GET /v1/me/commissions", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		if p.MemberUserID == "" {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		f, err := listFilter(r)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		rows, err := (repo.Repository{}).MemberStatements(r.Context(), store.DB, scope, p.MemberUserID, f.After, f.Limit)
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
		for j := range rows {
			rows[j].Orders, err = (repo.Repository{}).MemberOrders(r.Context(), store.DB, scope, rows[j].ID, p.MemberUserID)
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			switch rows[j].ReviewStatus {
			case "PENDING":
				rows[j].ReviewStatus = "WAITING_REVIEW"
			case "REJECTED", "CANCELLED":
				rows[j].ReviewStatus = "CANCELLED"
			case "APPROVED":
				rows[j].ReviewStatus = "APPROVED"
			default:
				rows[j].ReviewStatus = "PREPARING"
			}
		}
		result, _ := response.Result(200, "member-commissions", rid, map[string]any{"rows": rows, "next_after_id": next})
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/me/commission-orders/{id}/claim", require("member:claim", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		if p.MemberUserID == "" {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		var in app.Claim
		if err := httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Claim(r.Context(), p, r.PathValue("id"), in, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
}
