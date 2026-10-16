package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectoryThreeLevelsStatisticsPrivacyPagination(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("MySQL required")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: "test-directory-" + newID(), App: "app"}
	defer func() {
		rows, e := store.DB.Query(`SELECT DISTINCT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		tables := []string{}
		for rows.Next() {
			var v string
			rows.Scan(&v)
			tables = append(tables, v)
		}
		rows.Close()
		for _, v := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+v+"` WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		user := "u" + string(rune('0'+i))
		agent := "a" + string(rune('0'+i))
		if _, err = store.DB.Exec(`INSERT INTO users_external VALUES(?,?,?,?,?,?,?)`, scope.Tenant, scope.App, user, "sandbox", user, user, at); err != nil {
			t.Fatal(err)
		}
		if _, err = store.DB.Exec(`INSERT INTO agent_accounts VALUES(?,?,?,?,?,'ACTIVE',0,?)`, scope.Tenant, scope.App, agent, user, agent, at); err != nil {
			t.Fatal(err)
		}
		var parent any
		if i > 0 {
			parent = "a" + string(rune('0'+i-1))
		}
		if _, err = store.DB.Exec(`INSERT INTO agent_relations VALUES(?,?,?,?,?,NULL)`, scope.Tenant, scope.App, agent, parent, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.DB.Exec(`INSERT INTO users_external VALUES(?,?,'ordinary','sandbox','ordinary','普通会员',?)`, scope.Tenant, scope.App, at); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`INSERT INTO user_agent_bindings VALUES(?,?,'ordinary','a1',?,NULL,'fixture')`, scope.Tenant, scope.App, at); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_OPERATOR_TOKEN": strings.Repeat("o", 32), "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_TENANT": scope.Tenant, "LOCAL_APP": scope.App, "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(k, v)
	}
	salt := make([]byte, 16)
	hash, _ := pbkdf2.Key(sha256.New, "directory-password", salt, 600000, 32)
	accounts, _ := json.Marshal([]map[string]string{{"username": "member", "member_user_id": "u0", "salt": hex.EncodeToString(salt), "hash": hex.EncodeToString(hash)}})
	accountsFile := filepath.Join(t.TempDir(), "accounts.json")
	if err = os.WriteFile(accountsFile, accounts, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_LOCAL_ACCOUNTS_FILE", accountsFile)
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	transcript := []httpEvidence{}
	defer func() {
		if path := os.Getenv("DIRECTORY_HTTP_EVIDENCE_FILE"); path != "" {
			body, _ := json.Marshal(transcript)
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	call := func(method, path, token string, in any, signed bool, want int) map[string]any {
		t.Helper()
		var b []byte
		if in != nil {
			b, _ = json.Marshal(in)
		}
		r, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", newID())
		if signed {
			mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 32)))
			mac.Write(b)
			r.Header.Set("X-Source-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d want%d %s", method, path, res.StatusCode, want, raw)
		}
		template := strings.Split(path, "?")[0]
		if strings.HasPrefix(template, "/v1/agents/") {
			template = "/v1/agents/{id}/team"
		}
		transcript = append(transcript, httpEvidence{Method: method, Template: template, URL: path, Request: b, Response: raw, Status: res.StatusCode, ContentType: res.Header.Get("Content-Type")})
		var out struct {
			Data map[string]any `json:"data"`
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if e = d.Decode(&out); e != nil {
			t.Fatal(e)
		}
		return out.Data
	}
	operator := strings.Repeat("o", 32)
	checker := strings.Repeat("a", 32)
	for i, user := range []string{"u1", "u2", "ordinary"} {
		amount := []string{"100", "200", "400"}[i]
		in := map[string]any{"user_id": user, "currency": "USD", "observed_at": at.Add(time.Minute), "deposit_minor": amount, "withdrawal_minor": "0", "bet_minor": "500", "valid_bet_minor": "300", "vip": "VIP3", "login_ip": "::ffff:192.0.2.1"}
		call("POST", "/v1/member-statistics", operator, in, true, 200)
		call("POST", "/v1/member-statistics", operator, in, true, 200)
		in["deposit_minor"] = "999"
		call("POST", "/v1/member-statistics", operator, in, true, 409)
	}
	first := call("GET", "/v1/agents/a0/team?currency=USD&limit=1", operator, nil, false, 200)
	summary := first["summary"].(map[string]any)
	if summary["members"].(json.Number).String() != "4" || summary["missing_statistics"].(json.Number).String() != "1" || summary["deposit_minor"] != "700" || summary["complete"] != false {
		t.Fatal(first)
	}
	stamp := url.QueryEscape(first["as_of"].(string))
	cursor := first["next_after_id"].(string)
	second := call("GET", "/v1/agents/a0/team?currency=USD&limit=1&after_id="+cursor+"&as_of="+stamp, operator, nil, false, 200)
	if second["summary"].(map[string]any)["deposit_minor"] != "700" {
		t.Fatal("page summary changed", second)
	}
	all := call("GET", "/v1/agents/a0/team?depth=2&currency=USD", operator, nil, false, 200)
	if len(all["rows"].([]any)) != 2 {
		t.Fatal(all)
	}
	for _, v := range all["rows"].([]any) {
		r := v.(map[string]any)
		if r["statistics"].(map[string]any)["login_ip"] != "192.0.2.1" {
			t.Fatal(r)
		}
	}
	hidden := call("GET", "/v1/agents/a0/team?depth=2", checker, nil, false, 200)
	for _, v := range hidden["rows"].([]any) {
		if v.(map[string]any)["statistics"].(map[string]any)["login_ip"] != nil {
			t.Fatal("IP leaked", v)
		}
	}
	directory := call("GET", "/v1/agent-directory?limit=2", operator, nil, false, 200)
	if directory["summary"].(map[string]any)["members"].(json.Number).String() != "5" || len(directory["rows"].([]any)) != 2 {
		t.Fatal(directory)
	}
	call("GET", "/v1/agents/a0/team?depth=4", operator, nil, false, 400)
	call("GET", "/v1/agents/foreign/team", operator, nil, false, 404)
	login, e := server.Client().Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(`{"username":"member","password":"directory-password"}`))
	if e != nil {
		t.Fatal(e)
	}
	var signedIn map[string]any
	json.NewDecoder(login.Body).Decode(&signedIn)
	login.Body.Close()
	if login.StatusCode != 200 {
		t.Fatal(signedIn)
	}
	team := call("GET", "/v1/me/team?currency=USD", signedIn["access_token"].(string), nil, false, 200)
	if len(team["rows"].([]any)) != 4 {
		t.Fatal(team)
	}
	for _, v := range team["rows"].([]any) {
		if stats := v.(map[string]any)["statistics"]; stats != nil && stats.(map[string]any)["login_ip"] != nil {
			t.Fatal("member IP leak", v)
		}
	}
	call("POST", "/v1/member-statistics", operator, map[string]any{}, false, 403)
	t.Log("three physical levels incl ordinary member, no fourth level, cumulative statistics, full summary across pages, missing-data and sensitive-IP permissions PASS")
}
