// Package walletsandbox 对接独立测试钱包；不承担真实资金派发。
package walletsandbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	routersdk "github.com/ex-business/ex-service-router/sdk"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Binding struct {
	Site        string `json:"site_id"`
	Tenant      string `json:"tenant_id"`
	App         string `json:"app_id"`
	Client      string `json:"client_id"`
	Key         string `json:"key"`
	CallbackKey string `json:"callback_key"`
}
type Config struct {
	Endpoint string    `json:"endpoint"`
	Sites    []Binding `json:"sites"`
}

func Load(path string) (Config, error) {
	var c Config
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return c, errors.New("钱包接入配置必须为私有常规文件")
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &c) != nil {
		return c, errors.New("钱包配置格式无效")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("模拟钱包必须为固定本机地址")
	}
	scopes := map[string]bool{}
	clients := map[string]bool{}
	sites := map[string]bool{}
	if len(c.Sites) == 0 {
		return foundation.ErrValidation
	}
	for _, b := range c.Sites {
		k := b.Tenant + "\x00" + b.App
		if !foundation.ValidID(b.Site) || !foundation.ValidID(b.Tenant) || !foundation.ValidID(b.App) || !foundation.ValidID(b.Client) || len(b.Key) < 32 || len(b.CallbackKey) < 32 || scopes[k] || clients[b.Client] || sites[b.Site] {
			return foundation.ErrValidation
		}
		scopes[k] = true
		clients[b.Client] = true
		sites[b.Site] = true
	}
	return nil
}

type HTTPError struct{ Status int }

func (e HTTPError) Error() string { return fmt.Sprintf("钱包请求未成功：HTTP %d", e.Status) }

type Provider struct {
	Config Config
	Scope  foundation.Scope
}

func (p Provider) Enabled() bool { return p.Config.Validate() == nil }
func (p Provider) binding(scope foundation.Scope) (Binding, error) {
	for _, b := range p.Config.Sites {
		if b.Tenant == scope.Tenant && b.App == scope.App {
			return b, nil
		}
	}
	return Binding{}, errors.New("钱包站点未接入")
}
func (p Provider) ForScope(s foundation.Scope) Provider { p.Scope = s; return p }
func (p Provider) ByClient(client string) (Provider, bool) {
	for _, b := range p.Config.Sites {
		if b.Client == client {
			return p.ForScope(foundation.Scope{Tenant: b.Tenant, App: b.App}), true
		}
	}
	return Provider{}, false
}

type response struct {
	Merchant string `json:"merchant_payment_id"`
	Site     string `json:"site_id"`
	Account  string `json:"account_id"`
	Currency string `json:"currency"`
	Amount   string `json:"minor_units"`
	Status   string `json:"status"`
	Receipt  string `json:"receipt_ref"`
	Callback string `json:"callback_id"`
}

func (r response) outcome() model.Outcome {
	var receipt *string
	if r.Receipt != "" {
		receipt = &r.Receipt
	}
	return model.Outcome{CallbackID: r.Callback, MerchantID: r.Merchant, Currency: r.Currency, Amount: r.Amount, Status: r.Status, Receipt: receipt}
}
func (p Provider) call(ctx context.Context, b Binding, method, path string, in any, out any) error {
	var raw []byte
	var e error
	if in != nil {
		raw, e = json.Marshal(in)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.Config.Endpoint, "/")+path, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(b.Key))
	mac.Write([]byte(strings.Join([]string{"wallet-sandbox-v1", b.Client, b.Site, stamp, method, path, string(raw)}, "\n")))
	req.Header.Set("X-Wallet-Site", b.Site)
	req.Header.Set("X-Wallet-Client", b.Client)
	req.Header.Set("X-Wallet-Time", stamp)
	req.Header.Set("X-Wallet-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// 内部钱包经固定授权的Router发现后直连；SDK保留签名正文且不自动重发资金写请求。
	resp, e := routersdk.Do(client, req)
	if e != nil {
		return errors.New("钱包请求失败，需按原单查询")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return HTTPError{Status: resp.StatusCode}
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return e
	}
	return json.Unmarshal(data, out)
}
func (p Provider) Submit(ctx context.Context, v model.Payment) (model.Outcome, error) {
	return p.order(ctx, v, true)
}
func (p Provider) Query(ctx context.Context, v model.Payment) (model.Outcome, error) {
	return p.order(ctx, v, false)
}

// order 的站点及收款会员仅由仓储补充；签名绑定原始单号、账号、币种和金额，不从请求头推断权限。
func (p Provider) order(ctx context.Context, v model.Payment, submit bool) (model.Outcome, error) {
	b, e := p.binding(foundation.Scope{Tenant: v.WalletTenant, App: v.WalletApp})
	if e != nil || v.WalletAccount == "" {
		return model.Outcome{}, errors.New("钱包作用域或收款会员缺失")
	}
	var out response
	if submit {
		e = p.call(ctx, b, "POST", "/v1/wallet/credits", map[string]string{"merchant_payment_id": v.MerchantID, "account_id": v.WalletAccount, "currency": v.Currency, "minor_units": v.Amount}, &out)
	} else {
		e = p.call(ctx, b, "GET", "/v1/wallet/credits/"+url.PathEscape(v.MerchantID), nil, &out)
		// 只有验签请求得到钱包明确404时，才允许使用完整原单重投；超时和5xx继续保持未知。
		var missing HTTPError
		if errors.As(e, &missing) && missing.Status == http.StatusNotFound {
			return p.order(ctx, v, true)
		}
	}
	if e != nil {
		return model.Outcome{}, e
	}
	if out.Site != b.Site || out.Account != v.WalletAccount || out.Merchant != v.MerchantID || out.Currency != v.Currency || out.Amount != v.Amount {
		return model.Outcome{}, errors.New("钱包回包与原单不匹配")
	}
	if out.Status != "UNKNOWN" && !model.Terminal(out.Status) {
		return model.Outcome{}, foundation.ErrValidation
	}
	return out.outcome(), nil
}

// VerifyCallback 先按固定客户端选择作用域，再验签和校验正文站点；不能由正文站点选择密钥。
func (p Provider) VerifyCallback(raw []byte, signature string) (model.Outcome, error) {
	b, e := p.binding(p.Scope)
	if e != nil {
		return model.Outcome{}, e
	}
	sig, e := hex.DecodeString(signature)
	mac := hmac.New(sha256.New, []byte(b.CallbackKey))
	mac.Write(raw)
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return model.Outcome{}, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	var out response
	if json.Unmarshal(raw, &out) != nil || out.Site != b.Site || !foundation.ValidID(out.Callback) || !foundation.ValidID(out.Merchant) || !model.Terminal(out.Status) {
		return model.Outcome{}, foundation.ErrValidation
	}
	return out.outcome(), nil
}
func (p Provider) Reconcile(ctx context.Context, day string) ([]model.Outcome, error) {
	b, e := p.binding(p.Scope)
	if e != nil {
		return nil, e
	}
	if _, e = time.Parse("2006-01-02", day); e != nil {
		return nil, foundation.ErrValidation
	}
	var data struct {
		Items []response `json:"items"`
	}
	if e = p.call(ctx, b, "GET", "/v1/wallet/reconciliation/"+day, nil, &data); e != nil {
		return nil, e
	}
	out := []model.Outcome{}
	for _, r := range data.Items {
		if r.Site != b.Site || !model.Terminal(r.Status) {
			return nil, foundation.ErrValidation
		}
		out = append(out, r.outcome())
	}
	return out, nil
}
