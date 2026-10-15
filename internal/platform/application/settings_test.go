package application

import (
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"testing"
	"time"
)

func TestSettingsRejectSecretsPrecisionChangesAndPartialPeriod(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	future := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	base := foundation.CurrencyCatalog{Version: "currency-v2", From: future, Currencies: []foundation.Currency{{Code: "USD", Exponent: 2, Format: "0.00", Channels: []string{"sandbox"}}, {Code: "BTC", Exponent: 8, Format: "0.00000000", Channels: []string{"sandbox"}}, {Code: "POINT", Exponent: 0, Format: "0", Channels: []string{"sandbox"}}}}
	raw, _ := json.Marshal(base)
	if _, _, _, err := ValidateSetting(SettingInput{Kind: "CURRENCY_CATALOG", Configuration: raw}, now); err != nil {
		t.Fatal(err)
	}
	base.Currencies[0].Exponent = 3
	raw, _ = json.Marshal(base)
	if _, _, _, err := ValidateSetting(SettingInput{Kind: "CURRENCY_CATALOG", Configuration: raw}, now); err == nil {
		t.Fatal("existing currency precision changed")
	}
	for _, raw := range []string{`{"component_type":"PAYMENT","component_id":"sandbox","enabled":true,"credential":"secret"}`, `{"component_type":"PAYMENT","component_id":"sandbox"}`, `{"component_type":"PAYMENT","component_id":"unknown","enabled":true}`} {
		if _, _, _, err := ValidateSetting(SettingInput{Kind: "COMPONENT_STATE", Configuration: json.RawMessage(raw)}, now); err == nil {
			t.Fatal("invalid component accepted")
		}
	}
	calendar := foundation.Calendar{Version: "UTC-v2", From: future.Add(time.Hour), Timezone: "UTC", Period: "DAILY"}
	raw, _ = json.Marshal(calendar)
	if _, _, _, err := ValidateSetting(SettingInput{Kind: "CALENDAR", Configuration: raw}, now); err == nil {
		t.Fatal("calendar split open period")
	}
}
