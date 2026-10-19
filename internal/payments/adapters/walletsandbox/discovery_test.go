package walletsandbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/ex-business/ExCommission/internal/payments/domain"
)

func TestWalletUsesDiscoveryAndPreservesSignedWrite(t *testing.T) {
	// SDK按进程加载一次，子进程隔离配置，避免其他原直连测试影响发现验证。
	if os.Getenv("TEST_WALLET_DISCOVERY_CHILD") == "1" {
		p := Provider{Config: Config{Endpoint: "http://127.0.0.1:1", Sites: []Binding{{Site: "1000", Tenant: "t", App: "a", Client: "commission", Key: strings.Repeat("k", 32), CallbackKey: strings.Repeat("c", 32)}}}}
		_, err := p.Submit(context.Background(), model.Payment{WalletTenant: "t", WalletApp: "a", WalletAccount: "member", MerchantID: "order", Currency: "USD", Amount: "100"})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var writes, lookups atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		raw, _ := io.ReadAll(r.Body)
		m := hmac.New(sha256.New, []byte(strings.Repeat("k", 32)))
		io.WriteString(m, strings.Join([]string{"wallet-sandbox-v1", "commission", "1000", r.Header.Get("X-Wallet-Time"), r.Method, r.URL.RequestURI(), string(raw)}, "\n"))
		if r.Method != "POST" || r.URL.Path != "/v1/wallet/credits" || r.Header.Get("X-Wallet-Signature") != hex.EncodeToString(m.Sum(nil)) || r.Header.Get("Authorization") != "" {
			t.Error("发现改写了业务签名或泄漏发现凭据")
		}
		json.NewEncoder(w).Encode(map[string]string{"site_id": "1000", "merchant_payment_id": "order", "account_id": "member", "currency": "USD", "minor_units": "100", "status": "SUCCEEDED"})
	}))
	defer wallet.Close()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		if r.URL.Path != "/v1/discovery" || r.URL.Query().Get("service") != "ex-wallet-sandbox" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("d", 32) {
			t.Error("发现目标或授权错误")
		}
		now := time.Now().UnixMilli()
		json.NewEncoder(w).Encode(map[string]any{"service": "ex-wallet-sandbox", "server_time_ms": now, "cache_ms": 1000, "instances": []any{map[string]any{"instance": "wallet", "address": wallet.URL, "expires_ms": now + 10000, "weight": 1}}})
	}))
	defer node.Close()
	raw, _ := json.Marshal(map[string]any{"nodes": []string{node.URL}, "token": strings.Repeat("d", 32), "bindings": map[string]string{"http://127.0.0.1:1": "ex-wallet-sandbox"}})
	file := filepath.Join(t.TempDir(), "discovery.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWalletUsesDiscoveryAndPreservesSignedWrite$")
	cmd.Env = append(os.Environ(), "TEST_WALLET_DISCOVERY_CHILD=1", "EX_ROUTER_DISCOVERY_CONFIG_FILE="+file)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if writes.Load() != 1 || lookups.Load() != 1 {
		t.Fatal("派发或发现次数不符", writes.Load(), lookups.Load())
	}
}
