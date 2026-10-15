package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
)

type SettingsRepository interface {
	Save(context.Context, db.Executor, foundation.Scope, string, string, time.Time, []byte) error
	SetComponent(context.Context, db.Executor, foundation.Scope, foundation.ComponentState, uint64) error
}
type SettingInput struct {
	Kind          string          `json:"kind"`
	Configuration json.RawMessage `json:"configuration"`
	Revision      uint64          `json:"expected_revision"`
	Reason        string          `json:"reason"`
}
type SettingsService struct {
	Commands db.Commands
	Repo     SettingsRepository
	Workflow workflow.Repository
	Now      func() time.Time
	ID       func() string
}

func strictConfig(body []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return foundation.ErrValidation
	}
	return nil
}
func requiredConfiguration(body []byte, keys ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return foundation.ErrValidation
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return foundation.ErrValidation
		}
	}
	return nil
}
func ValidateSetting(input SettingInput, now time.Time) (string, time.Time, []byte, error) {
	validTime := func(t time.Time) bool { return !t.Before(now) && t.Equal(t.UTC().Truncate(24*time.Hour)) }
	switch input.Kind {
	case "CURRENCY_CATALOG":
		if err := requiredConfiguration(input.Configuration, "version_id", "effective_from", "currencies"); err != nil {
			return "", time.Time{}, nil, err
		}
		var c foundation.CurrencyCatalog
		if err := strictConfig(input.Configuration, &c); err != nil {
			return "", time.Time{}, nil, err
		}
		if !foundation.ValidID(c.Version) || !validTime(c.From) || input.Revision != 0 {
			return "", time.Time{}, nil, foundation.ErrValidation
		}
		var fields struct {
			Currencies []json.RawMessage `json:"currencies"`
		}
		_ = json.Unmarshal(input.Configuration, &fields)
		for _, v := range fields.Currencies {
			if err := requiredConfiguration(v, "code", "exponent", "display_format", "payment_channels"); err != nil {
				return "", time.Time{}, nil, err
			}
		}
		expected := foundation.CurrencyExponents()
		seen := map[string]bool{}
		for _, v := range c.Currencies {
			exponent, ok := expected[v.Code]
			format := "0"
			for i := 0; i < exponent; i++ {
				if i == 0 {
					format += "."
				}
				format += "0"
			}
			if !ok || seen[v.Code] || v.Exponent != exponent || v.Format != format || len(v.Channels) != 1 || v.Channels[0] != "sandbox" {
				return "", time.Time{}, nil, errors.New("UNSUPPORTED_CAPABILITY")
			}
			seen[v.Code] = true
		}
		if len(seen) != len(expected) {
			return "", time.Time{}, nil, foundation.ErrValidation
		}
		body, _ := json.Marshal(c)
		return c.Version, c.From, body, nil
	case "CALENDAR":
		if err := requiredConfiguration(input.Configuration, "version_id", "effective_from", "timezone", "period"); err != nil {
			return "", time.Time{}, nil, err
		}
		var c foundation.Calendar
		if err := strictConfig(input.Configuration, &c); err != nil {
			return "", time.Time{}, nil, err
		}
		if !foundation.ValidID(c.Version) || !validTime(c.From) || c.Timezone != "UTC" || c.Period != "DAILY" || input.Revision != 0 {
			return "", time.Time{}, nil, errors.New("UNSUPPORTED_CAPABILITY")
		}
		body, _ := json.Marshal(c)
		return c.Version, c.From, body, nil
	case "COMPONENT_STATE":
		if err := requiredConfiguration(input.Configuration, "component_type", "component_id", "enabled"); err != nil {
			return "", time.Time{}, nil, err
		}
		var c foundation.ComponentState
		if err := strictConfig(input.Configuration, &c); err != nil {
			return "", time.Time{}, nil, err
		}
		if !((c.Type == "SOURCE" || c.Type == "PAYMENT") && c.ID == "sandbox" || c.Type == "NOTIFICATION" && c.ID == "local-inbox") {
			return "", time.Time{}, nil, errors.New("UNSUPPORTED_CAPABILITY")
		}
		body, _ := json.Marshal(c)
		return c.Type + ":" + c.ID, time.Time{}, body, nil
	default:
		return "", time.Time{}, nil, errors.New("UNSUPPORTED_CAPABILITY")
	}
}
func (s SettingsService) Request(ctx context.Context, p identity.Principal, input SettingInput, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Reason) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	object, _, normalized, err := ValidateSetting(input, s.Now())
	if err != nil {
		return foundation.CommandResult{}, err
	}
	input.Configuration = normalized
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "platform-setting", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		body, _ := json.Marshal(input)
		a := approval.Request{ID: s.ID(), Type: "PLATFORM_SETTING", Applicant: p.Subject, ObjectRef: object, ObjectHash: hash.Hash(body), Payload: body}
		if err := s.Workflow.Create(ctx, tx, scope, a, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s SettingsService) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var input SettingInput
	if err := json.Unmarshal(a.Payload, &input); err != nil {
		return err
	}
	id, from, body, err := ValidateSetting(input, now)
	if err != nil {
		return err
	}
	kind := "CURRENCY"
	if input.Kind == "CALENDAR" {
		kind = "CALENDAR"
	}
	if input.Kind == "COMPONENT_STATE" {
		var c foundation.ComponentState
		if err = json.Unmarshal(body, &c); err != nil {
			return err
		}
		return s.Repo.SetComponent(ctx, tx, scope, c, input.Revision)
	}
	return s.Repo.Save(ctx, tx, scope, kind, id, from, body)
}
