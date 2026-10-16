package runtime

import (
	"context"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	repo "github.com/ex-business/ExCommission/internal/security/adapters"
	app "github.com/ex-business/ExCommission/internal/security/application"
	"github.com/ex-business/ExCommission/internal/security/ports"
	"os"
)

func verifyStepUp(store *mysql.Store, ctx context.Context, p principal.Principal, action, key string, payload any) error {
	var secrets ports.Secrets = repo.ConfigSecrets{JSON: os.Getenv("AUTH_TOTP_SECRETS")}
	if path := os.Getenv("AUTH_TOTP_FILE"); path != "" {
		secrets = repo.FileSecrets{Path: path}
	}
	return (app.Service{UOW: store, Repo: repo.Repository{}, Secrets: secrets, Now: clockUTC}).Verify(ctx, p, action, key, payload)
}
