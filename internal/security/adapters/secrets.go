package adapters

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
)

type Entry struct {
	Tenant  string `json:"tenant_id"`
	App     string `json:"app_id"`
	Subject string `json:"subject_id"`
	Secret  string `json:"secret"`
}
type FileSecrets struct{ Path string }

func (f FileSecrets) Secret(_ context.Context, s foundation.Scope, subject string) ([]byte, error) {
	stat, err := os.Stat(f.Path)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return nil, errors.New("TOTP_NOT_CONFIGURED")
	}
	body, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, errors.New("TOTP_NOT_CONFIGURED")
	}
	return configuredSecret(body, s, subject)
}

type ConfigSecrets struct{ JSON string }

func (c ConfigSecrets) Secret(_ context.Context, s foundation.Scope, subject string) ([]byte, error) {
	return configuredSecret([]byte(c.JSON), s, subject)
}
func configuredSecret(body []byte, s foundation.Scope, subject string) ([]byte, error) {
	var err error
	var entries []Entry
	if json.Unmarshal(body, &entries) != nil {
		return nil, errors.New("TOTP_NOT_CONFIGURED")
	}
	var secret []byte
	for _, e := range entries {
		if e.Tenant == s.Tenant && e.App == s.App && e.Subject == subject {
			if secret != nil {
				return nil, errors.New("TOTP_NOT_CONFIGURED")
			}
			secret, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
			if err != nil || len(secret) < 20 {
				return nil, errors.New("TOTP_NOT_CONFIGURED")
			}
		}
	}
	if secret == nil {
		return nil, errors.New("TOTP_NOT_CONFIGURED")
	}
	return secret, nil
}
