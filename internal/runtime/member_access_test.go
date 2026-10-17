package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	access "github.com/ex-business/ExCommission/internal/memberaccess/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
)

func TestMemberSourceRegistrationBindingAndRevocation(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要本地 MySQL")
	}
	store, e := mysql.Open(context.Background(), os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if e = store.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	tenant := "test-member-source-" + newID()
	app := "commission"
	operator := strings.Repeat("o", 32)
	key := strings.Repeat("k", 32)
	defer func() {
		rows, err := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if err != nil {
			t.Error(err)
			return
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if err = rows.Scan(&table); err != nil {
				t.Error(err)
				break
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Error("测试清理表名无效")
				break
			}
			tables = append(tables, table)
		}
		if err = rows.Err(); err != nil {
			t.Error(err)
		}
		rows.Close()
		for _, table := range tables {
			if _, err = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	active := true
	external := "member-external"
	responseTenant := tenant
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"active": active, "sub": external, "iss": "issuer", "aud": "commission", "exp": time.Now().Add(time.Hour).Unix(), "tenant_id": responseTenant, "app_id": app, "source_id": "sandbox"})
	}))
	defer source.Close()
	cfg := access.Config{Endpoint: source.URL, ClientID: "client", Secret: strings.Repeat("s", 32), Issuer: "issuer", Audience: "commission", Source: "sandbox", LocalSandbox: true}
	raw, _ := json.Marshal(cfg)
	file := filepath.Join(t.TempDir(), "source.json")
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	for k, v := range map[string]string{"AUTH_MEMBER_SOURCE_FILE": file, "APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": app, "LOCAL_OPERATOR_TOKEN": operator, "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_REGISTRATION_ENABLED": "true", "MEMBER_INVITATION_SOURCE": "sandbox", "LOCAL_REGISTRATION_URL": "http://127.0.0.1:18286/", "LOCAL_REGISTRATION_KEY": key, "LOCAL_USERS_FILE": "", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(k, v)
	}
	handler, e := businessHandler(context.Background(), store)
	if e != nil {
		t.Fatal(e)
	}
	request := func(path, scope string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer ms_test")
		if scope != "" {
			r.Header.Set("X-Tenant-ID", scope)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("/v1/auth/me", ""); w.Code != 401 {
		t.Fatal("未注册来源必须拒绝", w.Code, w.Body)
	}
	event := map[string]any{"schema_version": "1", "source_id": "sandbox", "event_id": "registration-login-test", "external_user_id": external, "display_name": "真实来源会员", "registered_at": time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano), "invitation_code": nil}
	raw, _ = json.Marshal(event)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("registration-v1\n" + tenant + "\n" + app + "\nsandbox\n"))
	mac.Write(raw)
	r := httptest.NewRequest("POST", "/v1/registrations", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+operator)
	r.Header.Set("Idempotency-Key", "registration-login-test")
	r.Header.Set("X-Registration-Signature", hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var registration struct {
		Data struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &registration)
	w = request("/v1/auth/me", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var me struct {
		Subject     string   `json:"subject_id"`
		User        string   `json:"member_user_id"`
		Permissions []string `json:"permissions"`
	}
	json.Unmarshal(w.Body.Bytes(), &me)
	if me.User == "" || me.User != registration.Data.UserID || len(me.Permissions) != 3 {
		t.Fatal("绑定必须使用注册回执", w.Body)
	}
	if w = request("/v1/me/earnings", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	// 会员与来源端共用稳定九位码；不同幂等键并发生成也不能分叉。
	var wg sync.WaitGroup
	codes := make(chan string, 4)
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/me/referral-links", strings.NewReader("{}"))
			r.Header.Set("Authorization", "Bearer ms_test")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", newID())
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 201 {
				t.Error(w.Code, w.Body)
				return
			}
			var result struct {
				Data struct {
					Code string `json:"code"`
					URL  string `json:"url"`
				} `json:"data"`
			}
			json.Unmarshal(w.Body.Bytes(), &result)
			if !regexp.MustCompile(`^[0-9]{9}$`).MatchString(result.Data.Code) || !strings.Contains(result.Data.URL, "ic="+result.Data.Code) {
				t.Error("不是统一注册入口", w.Body)
			}
			codes <- result.Data.Code
		}()
	}
	wg.Wait()
	close(codes)
	stable := ""
	for code := range codes {
		if stable != "" && stable != code {
			t.Fatal("并发分享码分叉", stable, code)
		}
		stable = code
	}
	if stable == "" {
		t.Fatal("未生成分享码")
	}
	w = request("/v1/me/referral-link", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), stable) {
		t.Fatal("分享查询不一致", w.Code, w.Body)
	}
	if w = request("/v1/payments/not-owned", ""); w.Code != 403 {
		t.Fatal("会员不能获得管理权限", w.Code, w.Body)
	}
	if w = request("/v1/me/earnings", "other-tenant"); w.Code != 403 {
		t.Fatal("跨站请求通过", w.Code)
	}
	if _, e = store.DB.Exec(`DELETE FROM identity_grants WHERE tenant_id=? AND app_id=? AND subject_id=? AND permission='member:read'`, tenant, app, me.Subject); e != nil {
		t.Fatal(e)
	}
	if w = request("/v1/me/earnings", ""); w.Code != 403 {
		t.Fatal("登录恢复了已撤销权限", w.Code, w.Body)
	}
	responseTenant = "forged-site"
	if w = request("/v1/auth/me", ""); w.Code != 401 {
		t.Fatal("来源跨站身份通过", w.Code)
	}
	responseTenant = tenant
	active = false
	if w = request("/v1/auth/me", ""); w.Code != 401 {
		t.Fatal("撤销令牌通过", w.Code)
	}
}
