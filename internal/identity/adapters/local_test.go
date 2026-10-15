package adapters

import (
	"context"
	"errors"
	"github.com/ex-business/ExCommission/internal/identity/domain"
	"strings"
	"testing"
)

func TestTrustedLocalIdentity(t *testing.T) {
	token := strings.Repeat("a", 32)
	principal := domain.Principal{Tenant: "tenant", App: "app", Subject: "subject", Permissions: []string{"read"}}
	provider := NewLocalProvider(map[string]domain.Principal{token: principal, "short": principal})
	for _, header := range []string{"", token, "Bearer short", "Bearer " + strings.Repeat("b", 32)} {
		if _, err := provider.Authenticate(context.Background(), header); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("accepted %q", header)
		}
	}
	identity, err := provider.Authenticate(context.Background(), "Bearer "+token)
	if err != nil || identity.Subject != "subject" {
		t.Fatal(err)
	}
	if err = identity.Authorize("other", "app", "read"); !errors.Is(err, domain.ErrPermission) {
		t.Fatal("cross tenant")
	}
	if err = identity.Authorize("tenant", "app", "write"); !errors.Is(err, domain.ErrPermission) {
		t.Fatal("privilege escalation")
	}
}
