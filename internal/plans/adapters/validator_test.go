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
