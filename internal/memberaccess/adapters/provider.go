// Package adapters 通过受信会员项目内省令牌，保持原有静态身份提供方独立。
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	routersdk "github.com/ex-business/ex-service-router/sdk"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/ports"
	ports "github.com/ex-business/ExCommission/internal/memberaccess/ports"
)

type Config struct {
	Endpoint string `json:"endpoint"`
	ClientID string `json:"client_id"`
	Secret   string `json:"client_secret"`
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	Source   string `json:"source_id"`
	// HTTP 只允许显式选择的本地沙箱，且必须为字面量回环地址；正式来源仍要求 HTTPS。
	LocalSandbox bool `json:"local_sandbox"`
}
type Provider struct {
	Config      Config
	Tenant, App string
	Resolve     ports.Resolver
	Fallback    identity.Provider
	Client      *http.Client
	Now         func() time.Time
}

func New(c Config, tenant, app string, resolve ports.Resolver, fallback identity.Provider) (*Provider, error) {
	u, e := url.Parse(c.Endpoint)
	valid := e == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
	if valid && u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		valid = c.LocalSandbox && u.Scheme == "http" && ip != nil && ip.IsLoopback()
	}
	if !valid || c.ClientID == "" || len(c.Secret) < 32 || c.Issuer == "" || c.Audience == "" || c.Source == "" || tenant == "" || app == "" || resolve == nil {
		return nil, errors.New("会员身份来源配置无效")
	}
	return &Provider{Config: c, Tenant: tenant, App: app, Resolve: resolve, Fallback: fallback, Client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Now: time.Now}, nil
}
func (p *Provider) Authenticate(ctx context.Context, header string) (principal.Principal, error) {
	if !strings.HasPrefix(header, "Bearer ms_") {
		if p.Fallback != nil {
			return p.Fallback.Authenticate(ctx, header)
		}
		return principal.Principal{}, principal.ErrUnauthenticated
	}
	if len(header) > 256 {
		return principal.Principal{}, principal.ErrUnauthenticated
	}
	// 来源范围在服务端配置固定，不能使用内省响应中的站点或用户ID直接构造权限。
	form := url.Values{"token": {strings.TrimPrefix(header, "Bearer ")}}
	req, e := http.NewRequestWithContext(ctx, "POST", p.Config.Endpoint, strings.NewReader(form.Encode()))
	if e != nil {
		return principal.Principal{}, errors.New("会员身份来源不可用")
	}
	req.SetBasicAuth(p.Config.ClientID, p.Config.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := routersdk.Do(p.Client, req)
	if e != nil {
		return principal.Principal{}, errors.New("会员身份来源不可用")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return principal.Principal{}, errors.New("会员身份来源不可用")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(b) > 65536 {
		return principal.Principal{}, errors.New("会员身份响应无效")
	}
	var v struct {
		Active   bool   `json:"active"`
		Subject  string `json:"sub"`
		Issuer   string `json:"iss"`
		Audience string `json:"aud"`
		Expires  int64  `json:"exp"`
		Tenant   string `json:"tenant_id"`
		App      string `json:"app_id"`
		Source   string `json:"source_id"`
	}
	if json.Unmarshal(b, &v) != nil {
		return principal.Principal{}, errors.New("会员身份响应无效")
	}
	if !v.Active || v.Subject == "" || v.Issuer != p.Config.Issuer || v.Audience != p.Config.Audience || v.Expires <= p.Now().Unix() || v.Tenant != p.Tenant || v.App != p.App || v.Source != p.Config.Source {
		return principal.Principal{}, principal.ErrUnauthenticated
	}
	result, e := p.Resolve(ctx, p.Config.Source, v.Subject)
	if e != nil {
		return principal.Principal{}, e
	}
	if result.Tenant != p.Tenant || result.App != p.App || result.MemberUserID == "" || !strings.HasPrefix(result.Subject, "member-") {
		return principal.Principal{}, principal.ErrUnauthenticated
	}
	return result, nil
}
