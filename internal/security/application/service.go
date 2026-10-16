package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/security/domain"
	"github.com/ex-business/ExCommission/internal/security/ports"
	"time"
)

type Service struct {
	UOW     db.UnitOfWork
	Repo    ports.Repository
	Secrets ports.Secrets
	Now     func() time.Time
}

func (s Service) Verify(ctx context.Context, p identity.Principal, action, key string, payload any) error {
	code := model.CodeFrom(ctx)
	if code == "" {
		return errors.New("TOTP_REQUIRED")
	}
	if key == "" || len(key) > 128 || p.Subject == "" {
		return foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	secret, err := s.Secrets.Secret(ctx, scope, p.Subject)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Action, Key string
		Payload     any
	}{action, key, payload})
	if err != nil {
		return err
	}
	body, err = foundation.BusinessCanonicalJSON(body)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	at := s.Now()
	var failure error
	err = s.UOW.Within(foundation.WithScope(ctx, scope), func(tx db.Executor) error {
		attempts, err := s.Repo.Attempt(ctx, tx, scope, p.Subject, at.Unix()/60)
		if err != nil {
			return err
		}
		if attempts > 5 {
			failure = errors.New("TOTP_RATE_LIMITED")
			return nil
		}
		counter, err := model.Match(secret, code, at)
		if err != nil {
			failure = err
			return nil
		}
		if err = s.Repo.Consume(ctx, tx, scope, p.Subject, counter, hash); err != nil {
			if err.Error() == "TOTP_REPLAY" {
				failure = err
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return failure
}
