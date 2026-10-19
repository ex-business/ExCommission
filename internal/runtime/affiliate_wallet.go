package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flowapp "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	wallet "github.com/ex-business/ExCommission/internal/affiliatewallet"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	sandbox "github.com/ex-business/ExCommission/internal/payments/adapters/walletsandbox"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

func affiliateWalletConfig() (sandbox.Config, error) {
	mode := os.Getenv("AFFILIATE_CLAIM_WALLET")
	if mode == "" || mode == "internal" {
		return sandbox.Config{}, nil
	}
	if mode != "wallet-sandbox" {
		return sandbox.Config{}, errors.New("代理领取钱包模式无效")
	}
	c, e := sandbox.Load(os.Getenv("WALLET_SANDBOX_CONFIG_FILE"))
	if e != nil {
		return c, e
	}
	for _, b := range c.Sites {
		if len(b.Site) > 100 || len(b.Client) > 100 {
			return c, foundation.ErrValidation
		}
	}
	return c, nil
}
func configureAffiliateWalletFunds(s flowapp.Service) flowapp.Service {
	if os.Getenv("AFFILIATE_CLAIM_WALLET") != "wallet-sandbox" {
		return s
	}
	// 校验配置由安装入口执行；作用域校验仍在资金事务中，错误站点不能先占用权益。
	s.Funds = wallet.Funds{Funds: s.Funds.(flowrepo.Funds), Binding: func(sc foundation.Scope) (string, string, error) {
		c, e := affiliateWalletConfig()
		if e != nil {
			return "", "", e
		}
		return affiliateWalletBinding(c, sc)
	}, ValidateScope: func(sc foundation.Scope) error {
		c, e := affiliateWalletConfig()
		if e != nil {
			return e
		}
		for _, b := range c.Sites {
			if b.Tenant == sc.Tenant && b.App == sc.App {
				return nil
			}
		}
		return errors.New("钱包站点未接入")
	}}
	return s
}
func affiliateWalletBinding(c sandbox.Config, sc foundation.Scope) (string, string, error) {
	for _, b := range c.Sites {
		if b.Tenant == sc.Tenant && b.App == sc.App {
			return b.Site, b.Client, nil
		}
	}
	return "", "", errors.New("钱包站点未接入")
}
func affiliateWalletService(store *mysql.Store, c sandbox.Config) wallet.Service {
	return wallet.Service{UOW: store, Query: store.DB, Provider: sandbox.Provider{Config: c}, Binding: func(sc foundation.Scope) (string, string, error) { return affiliateWalletBinding(c, sc) }}
}
func recoverAffiliateWallet(ctx context.Context, store *mysql.Store) error {
	c, e := affiliateWalletConfig()
	if e != nil {
		return e
	}
	s := affiliateWalletService(store, c)
	for _, b := range c.Sites {
		if e = s.Recover(ctx, foundation.Scope{Tenant: b.Tenant, App: b.App}); e != nil {
			return e
		}
	}
	return nil
}
func installAffiliateWallet(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) error {
	c, e := affiliateWalletConfig()
	if e != nil {
		return e
	}
	if len(c.Sites) == 0 {
		return nil
	}
	s := affiliateWalletService(store, c)
	provider := sandbox.Provider{Config: c}
	// 回执只由配置中的客户端映射站点并验签；正文自报站点不用于选密钥或权限。
	mux.HandleFunc("POST /v1/payment-callbacks/affiliate-wallet/{client_id}", func(w http.ResponseWriter, r *http.Request) {
		rid := newID()
		bound, ok := provider.ByClient(r.PathValue("client_id"))
		if !ok {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		outcome, e := bound.VerifyCallback(raw, r.Header.Get("X-Payment-Signature"))
		if e == nil {
			var owned bool
			owned, e = s.Owns(r.Context(), bound.Scope, outcome.MerchantID)
			if e == nil && owned {
				e = s.Apply(r.Context(), bound.Scope, outcome)
			} else if e == nil {
				// 共用客户端只有一个回执地址；非代理原单必须再次经过旧渠道绑定及验签，禁止以业务失败触发降级。
				legacy := paymentService(store)
				oldProvider, exists := legacy.Providers["wallet-sandbox"].(sandbox.Provider)
				if !exists {
					e = foundation.ErrValidation
				} else if oldBound, exists := oldProvider.ByClient(r.PathValue("client_id")); !exists || oldBound.Scope != bound.Scope {
					e = foundation.ErrValidation
				} else {
					oldOutcome, verifyErr := oldBound.VerifyCallback(raw, r.Header.Get("X-Payment-Signature"))
					e = verifyErr
					if e == nil {
						e = legacy.Apply(r.Context(), oldBound.Scope, "wallet-sandbox", oldOutcome)
					}
				}
			}
		}
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for _, path := range []string{"/v1/me/affiliate/claim-deliveries", "/v1/affiliate/claim-deliveries"} {
		member := path == "/v1/me/affiliate/claim-deliveries"
		permission := "affiliate:read"
		if member {
			permission = "member:read"
		}
		mux.Handle("GET "+path, require(permission, func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			user := ""
			if member {
				user = p.MemberUserID
				if user == "" {
					httpapi.Error(w, rid, principal.ErrPermission)
					return
				}
			}
			limit := 20
			if text := r.URL.Query().Get("limit"); text != "" {
				var e error
				limit, e = strconv.Atoi(text)
				if e != nil {
					httpapi.Error(w, rid, foundation.ErrValidation)
					return
				}
			}
			after := r.URL.Query().Get("after_id")
			if limit < 1 || limit > 100 || (after != "" && !foundation.ValidID(after)) {
				httpapi.Error(w, rid, foundation.ErrValidation)
				return
			}
			rows, e := s.List(r.Context(), foundation.Scope{Tenant: p.Tenant, App: p.App}, user, limit+1, after)
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			more := len(rows) > limit
			if more {
				rows = rows[:limit]
			}
			next := ""
			if more {
				next = rows[len(rows)-1].ID
			}
			result, _ := response.Result(200, "affiliate-wallet-deliveries", rid, map[string]any{"rows": rows, "has_more": more, "next_after": next, "limit": limit})
			httpapi.Write(w, result)
		}))
	}
	return nil
}
