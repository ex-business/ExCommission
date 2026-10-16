package adapters

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ex-business/ExCommission/internal/identity/domain"
)

func testAccount(t *testing.T) LoginAccount {
	t.Helper()
	salt := make([]byte, 16)
	hash, e := pbkdf2.Key(sha256.New, "correct-password", salt, 600000, 32)
	if e != nil {
		t.Fatal(e)
	}
	return LoginAccount{Username: "operator", Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(hash), Principal: domain.Principal{Subject: "local-operator", Tenant: "t", App: "a"}}
}
func TestPasswordSessionsExpiryLogoutAndLimits(t *testing.T) {
	s, e := NewSessions([]LoginAccount{testAccount(t)}, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	s.Now = func() time.Time { return now }
	for _, name := range []string{"operator", "unknown"} {
		if _, _, e = s.Login(name, "bad"); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal(e)
		}
	}
	token, p, e := s.Login("operator", "correct-password")
	if e != nil || p.Subject != "local-operator" {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(context.Background(), "Bearer "+token); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := s.Authenticate(context.Background(), "Bearer "+token); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	s.Logout("Bearer " + token)
	if _, e = s.Authenticate(context.Background(), "Bearer "+token); !errors.Is(e, domain.ErrUnauthenticated) {
		t.Fatal(e)
	}
	token, _, e = s.Login("operator", "correct-password")
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(8 * time.Hour)
	if _, e = s.Authenticate(context.Background(), "Bearer "+token); !errors.Is(e, domain.ErrUnauthenticated) {
		t.Fatal(e)
	}
	for range 10 {
		_, _, _ = s.Login("random-name", "bad")
	}
	if _, _, e = s.Login("another-name", "bad"); !errors.Is(e, ErrLoginRate) {
		t.Fatal(e)
	}
	if len(s.attempts) > 3 {
		t.Fatal("unbounded limiter")
	}
	now = now.Add(time.Minute)
	if _, _, e = s.Login("operator", "correct-password"); e != nil {
		t.Fatal(e)
	}
	if _, e = NewSessions([]LoginAccount{{Username: "x"}}, nil, nil); e == nil {
		t.Fatal("invalid hashes accepted")
	}
}
func TestThirdPartyIntrospectionBoundIdentityAndFailClosed(t *testing.T) {
	now := time.Now()
	status := 200
	response := map[string]any{"active": true, "iss": "issuer", "aud": []string{"commission"}, "sub": "third-user", "exp": now.Add(time.Minute).Unix()}
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		u, p, ok := r.BasicAuth()
		if !ok || u != "client" || p != "secret" || r.Method != "POST" {
			t.Error("bad client auth")
		}
		_ = r.ParseForm()
		if r.Form.Get("token") != "external" {
			t.Error("wrong token")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	c := ExternalConfig{Endpoint: server.URL, ClientID: "client", ClientSecret: "secret", Issuer: "issuer", Audience: "commission", Bindings: map[string]string{"third-user": "local-operator"}}
	provider, e := NewIntrospection(c, map[string]domain.Principal{"local-operator": {Subject: "local-operator", Tenant: "trusted", App: "a"}})
	if e != nil {
		t.Fatal(e)
	}
	provider.Client = server.Client()
	provider.Now = func() time.Time { return now }
	p, e := provider.Authenticate(context.Background(), "Bearer external")
	if e != nil || p.Tenant != "trusted" || len(p.Permissions) != 0 {
		t.Fatal(p, e)
	}
	for _, test := range []struct {
		field string
		value any
	}{{"active", false}, {"iss", "evil"}, {"aud", "other"}, {"sub", "unmapped"}, {"exp", now.Unix()}} {
		old := response[test.field]
		response[test.field] = test.value
		if _, e = provider.Authenticate(context.Background(), "Bearer external"); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatalf("%s: %v", test.field, e)
		}
		response[test.field] = old
	}
	status = 503
	if _, e = provider.Authenticate(context.Background(), "Bearer external"); e == nil || errors.Is(e, domain.ErrUnauthenticated) {
		t.Fatal("provider failure not distinct")
	}
	if calls != 7 {
		t.Fatal(calls)
	}
	c.Endpoint = "http://example.com"
	if _, e = NewIntrospection(c, provider.Principals); e == nil {
		t.Fatal("plaintext endpoint accepted")
	}
}
