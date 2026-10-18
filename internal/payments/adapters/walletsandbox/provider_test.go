package walletsandbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestAndCallbackScope(t *testing.T) {
	key := strings.Repeat("k", 32)
	cb := strings.Repeat("c", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Wallet-Site") != "1000" || r.Header.Get("X-Wallet-Client") != "commission" {
			t.Error("缺少站点绑定")
		}
		json.NewEncoder(w).Encode(map[string]string{"site_id": "1000", "merchant_payment_id": "order", "account_id": "member", "currency": "USD", "minor_units": "100", "status": "SUCCEEDED", "receipt_ref": "receipt"})
	}))
	defer srv.Close()
	p := Provider{Config: Config{Endpoint: srv.URL, Sites: []Binding{{Site: "1000", Tenant: "t", App: "a", Client: "commission", Key: key, CallbackKey: cb}}}}
	payment := model.Payment{WalletTenant: "t", WalletApp: "a", WalletAccount: "member", MerchantID: "order", Currency: "USD", Amount: "100"}
	if _, e := p.Submit(context.Background(), payment); e != nil {
		t.Fatal(e)
	}
	payment.WalletTenant = "other"
	if _, e := p.Submit(context.Background(), payment); e == nil {
		t.Fatal("跨站未拒绝")
	}
	raw := []byte(`{"site_id":"1000","callback_id":"callback","merchant_payment_id":"order","currency":"USD","minor_units":"100","status":"SUCCEEDED"}`)
	mac := hmac.New(sha256.New, []byte(cb))
	mac.Write(raw)
	sig := hex.EncodeToString(mac.Sum(nil))
	if _, e := p.VerifyCallback(raw, sig); e == nil {
		t.Fatal("未绑定站点也能接收回执")
	}
	p = p.ForScope(foundation.Scope{Tenant: "t", App: "a"})
	if _, e := p.VerifyCallback(raw, sig); e != nil {
		t.Fatal(e)
	}
	if _, e := p.VerifyCallback(raw, "wrong"); e == nil {
		t.Fatal("错误签名未拒绝")
	}
}

func TestQueryMissingResubmitsOnlyOriginalOrder(t *testing.T) {
	submits := 0
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reads++
			w.WriteHeader(404)
			return
		}
		submits++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["merchant_payment_id"] != "same-order" || in["account_id"] != "same-member" || in["minor_units"] != "123" {
			t.Error("重试改变了原单")
		}
		json.NewEncoder(w).Encode(map[string]string{"site_id": "1000", "merchant_payment_id": "same-order", "account_id": "same-member", "currency": "USD", "minor_units": "123", "status": "SUCCEEDED"})
	}))
	defer srv.Close()
	p := Provider{Config: Config{Endpoint: srv.URL, Sites: []Binding{{Site: "1000", Tenant: "t", App: "a", Client: "commission", Key: strings.Repeat("k", 32), CallbackKey: strings.Repeat("c", 32)}}}}
	_, e := p.Query(context.Background(), model.Payment{WalletTenant: "t", WalletApp: "a", WalletAccount: "same-member", MerchantID: "same-order", Currency: "USD", Amount: "123"})
	if e != nil || reads != 1 || submits != 1 {
		t.Fatal(e, reads, submits)
	}
}
