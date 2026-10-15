package adapters

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestSchemaAndDomainValidation(t *testing.T) {
	validator, err := NewValidator(map[string]int{"USD": 2, "BTC": 8, "POINT": 0}, map[string]bool{"UTC-v1": true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, err := validator.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, roundtrip, err := validator.Decode(canonical); err != nil || !bytes.Equal(canonical, roundtrip) {
		t.Fatalf("config did not round trip %v", err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = json.RawMessage(`true`)
	invalid, _ := json.Marshal(object)
	if _, _, err = validator.Decode(invalid); err == nil {
		t.Fatal("unknown configuration accepted")
	}
}

func TestSemanticNormalizationAndExplicitFundingLimit(t *testing.T) {
	validator, err := NewValidator(map[string]int{"USD": 2}, map[string]bool{"UTC-v1": true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	rule := object["rules"].([]any)[0].(map[string]any)
	rule["algorithm_id"] = "FIXED"
	rule["parameters"] = map[string]any{"amount_minor": "00010", "reward_currency": "USD"}
	rule["funding_limit_ppm"] = nil
	object["event_budget_minor"] = "10"
	first, _ := json.Marshal(object)
	_, normalized, err := validator.Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	rule["parameters"].(map[string]any)["amount_minor"] = "10"
	second, _ := json.Marshal(object)
	_, again, err := validator.Decode(second)
	if err != nil || !bytes.Equal(normalized, again) {
		t.Fatal("equivalent amounts did not canonicalize", err)
	}
	rule["algorithm_id"] = "PERCENT"
	rule["recipient"] = "ANCESTORS"
	rule["parameters"] = map[string]any{"rate_ppm": "500000"}
	rule["funding_limit_ppm"] = "1500000"
	object["event_budget_minor"] = "150000"
	encoded, _ := json.Marshal(object)
	if _, _, err = validator.Decode(encoded); err != nil {
		t.Fatalf("single rates within100%% with explicit total funding rejected %v", err)
	}
}
