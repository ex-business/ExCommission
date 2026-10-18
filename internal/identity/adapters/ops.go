package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	routersdk "github.com/ex-business/ex-service-router/sdk"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/identity/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

type OpsSite struct {
	Tenant string `json:"tenant_id"`
	App    string `json:"app_id"`
}
type OpsConfig struct {
	Endpoint string             `json:"endpoint"`
	Secret   string             `json:"secret"`
	Sites    map[string]OpsSite `json:"sites,omitempty"`
}
type OpsProvider struct {
	Config OpsConfig
	Legacy ports.Provider
	Client *http.Client
}

func LoadOpsProvider(path string, legacy ports.Provider) (ports.Provider, error) {
	if path == "" {
		return legacy, nil
	}
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return nil, errors.New("运营身份配置必须为私有常规文件")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var c OpsConfig
	if json.Unmarshal(raw, &c) != nil {
		return nil, errors.New("运营身份配置格式无效")
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || (u.Path != "/internal/commission/introspect" && u.Path != "/internal/identity/introspect") || u.RawQuery != "" || u.Fragment != "" || u.User != nil || len(c.Secret) < 32 {
		return nil, errors.New("运营身份接入地址或密钥无效")
	}
	if u.Path == "/internal/identity/introspect" && len(c.Sites) == 0 {
		return nil, errors.New("业务站点映射未配置")
	}
	for site, binding := range c.Sites {
		if !foundation.ValidID(site) || !foundation.ValidID(binding.Tenant) || !foundation.ValidID(binding.App) {
			return nil, errors.New("业务站点映射无效")
		}
	}
	return &OpsProvider{c, legacy, &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *OpsProvider) Authenticate(ctx context.Context, header string) (domain.Principal, error) {
	if !strings.HasPrefix(header, "Bearer ops1.") {
		if p.Legacy != nil {
			return p.Legacy.Authenticate(ctx, header)
		}
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if len(token) > 4096 {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	v, e := p.call(ctx, p.Config.Endpoint, map[string]string{"token": token})
	if e != nil {
		return domain.Principal{}, e
	}
	var out struct {
		Subject     string   `json:"subject"`
		Tenant      string   `json:"tenant_id"`
		App         string   `json:"app_id"`
		Permissions []string `json:"permissions"`
		Expires     int64    `json:"expires"`
		Site        string   `json:"site_id"`
		Roles       []string `json:"roles"`
	}
	if json.Unmarshal(v, &out) != nil {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if strings.HasSuffix(p.Config.Endpoint, "/identity/introspect") {
		binding, ok := p.Config.Sites[out.Site]
		if !ok {
			return domain.Principal{}, domain.ErrUnauthenticated
		}
		out.Tenant = binding.Tenant
		out.App = binding.App
		out.Permissions = OpsPermissions(out.Roles)
	}
	if !strings.HasPrefix(out.Subject, "ops-") || out.Tenant == "" || out.App == "" || out.Expires <= time.Now().Unix() {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	return domain.Principal{Tenant: out.Tenant, App: out.App, Subject: out.Subject, Permissions: out.Permissions, OpsCredential: token, OpsTenant: out.Tenant, OpsApp: out.App}, nil
}
func (p *OpsProvider) call(ctx context.Context, endpoint string, body map[string]string) ([]byte, error) {
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+p.Config.Secret)
	req.Header.Set("Content-Type", "application/json")
	res, e := routersdk.Do(p.Client, req)
	if e != nil {
		return nil, errors.New("运营身份服务不可用")
	}
	defer res.Body.Close()
	v, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(v) > 65536 {
		return nil, errors.New("运营身份服务响应无效")
	}
	if strings.HasSuffix(endpoint, "step-up") && (res.StatusCode == 403 || res.StatusCode == 429) {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(v, &failure) == nil && (failure.Error == "TOTP_INVALID" || failure.Error == "TOTP_REPLAY" || failure.Error == "TOTP_RATE_LIMITED") {
			return nil, errors.New(failure.Error)
		}
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, domain.ErrUnauthenticated
	}
	if res.StatusCode != 200 {
		return nil, errors.New("运营身份服务拒绝请求")
	}
	return v, nil
}

// 代理审批只委托当前真实运营身份的二次验证，服务不可用时拒绝，不能退回共享身份。
func OpsStepUp(ctx context.Context, path string, p domain.Principal, code, action, key string, payload any) error {
	provider, e := LoadOpsProvider(path, nil)
	if e != nil {
		return e
	}
	ops, ok := provider.(*OpsProvider)
	if !ok {
		return errors.New("运营身份接入未配置")
	}
	body, _ := json.Marshal(payload)
	endpoint := strings.TrimSuffix(ops.Config.Endpoint, "introspect") + "step-up"
	_, e = ops.call(ctx, endpoint, map[string]string{"token": p.OpsCredential, "code": code, "action": action, "key": key, "payload": string(body)})
	return e
}

// 业务权限由本系统按原申请人、审批人职责映射，身份接入不得收缩既有业务能力。
// 会员本人、签名来源和运营账号管理继续走各自系统，不映射member、affiliate:ingest或identity权限。
func OpsPermissions(roles []string) []string {
	grants := map[string]bool{}
	for _, role := range roles {
		if role != "viewer" && role != "applicant" && role != "approver" {
			continue
		}
		for _, v := range strings.Fields("ingestion:read affiliate:read agents:read agents:directory workflow:read site:read plans:read users:read milestones:read statements:read payments:read refunds:read events:read tasks:read ledger:read reports:read risk:read audit:read platform:read") {
			grants[v] = true
		}
		if role == "applicant" {
			for _, v := range strings.Fields("ingestion:configure affiliate:configure affiliate:manage affiliate:export agents:sensitive:read agents:write agents:status:request relations:request grades:request grades:evaluate site:request plans:write plans:simulate users:sync users:statistics milestones:request milestones:write statements:write payments:write payments:verify payments:reconcile refunds:write events:write tasks:recover recoveries:request adjustments:request reports:read reports:export reports:download risk:write risk:release:request security:handle platform:request workflow:cancel workflow:decide") {
				grants[v] = true
			}
		}
		if role == "approver" {
			for _, v := range strings.Fields("ingestion:approve affiliate:approve affiliate:review affiliate:export agents:sensitive:read workflow:decide agents:status:approve relations:approve site:approve plans:approve milestones:approve statements:approve recoveries:approve adjustments:approve risk:approve platform:approve workflow:cancel workflow:recover") {
				grants[v] = true
			}
		}
	}
	out := []string{}
	for grant := range grants {
		out = append(out, grant)
	}
	return out
}
