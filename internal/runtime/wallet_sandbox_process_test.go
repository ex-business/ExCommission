package runtime

import (
	"context"
	"encoding/json"
	walletsandbox "github.com/ex-business/ExCommission/internal/payments/adapters/walletsandbox"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// prepareWalletProcess 启动独立钱包进程和独立账本，验证真正的HTTP调用与持久回执任务。
func prepareWalletProcess(t *testing.T, scope foundation.Scope, dir string) (string, func(string)) {
	t.Helper()
	binary := os.Getenv("TEST_WALLET_BINARY")
	if binary == "" {
		t.Fatal("需要TEST_WALLET_BINARY独立钱包程序")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	listener.Close()
	binding := walletsandbox.Binding{Site: "1000", Tenant: scope.Tenant, App: scope.App, Client: "commission-test", Key: newID() + newID(), CallbackKey: newID() + newID()}
	adapterConfig := walletsandbox.Config{Endpoint: "http://" + address, Sites: []walletsandbox.Binding{binding}}
	raw, _ := json.Marshal(adapterConfig)
	path := filepath.Join(dir, "wallet-adapter.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	return path, func(callbackURL string) {
		config := map[string]any{"listen": address, "ops_url": "http://127.0.0.1:19587", "ops_secret": newID() + newID(), "sites": []any{map[string]string{"site_id": binding.Site, "client_id": binding.Client, "key": binding.Key, "callback_url": callbackURL + "/v1/wallet-sandbox-callbacks/" + binding.Client, "callback_key": binding.CallbackKey}}}
		raw, _ := json.Marshal(config)
		path := filepath.Join(dir, "wallet-process.json")
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(binary, "-config", path, "-db", filepath.Join(dir, "external-wallet.db"))
		log, e := os.OpenFile(filepath.Join(dir, "wallet.log"), os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stdout = log
		cmd.Stderr = log
		if e = cmd.Start(); e != nil {
			log.Close()
			t.Fatal(e)
		}
		t.Cleanup(func() {
			cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
			}
			log.Close()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/", nil)
			resp, e := http.DefaultClient.Do(req)
			if e == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal("独立钱包启动失败")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
}
