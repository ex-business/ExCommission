package adapters

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/plan.schema.json
var planSchema []byte

type Validator struct {
	schema    *jsonschema.Schema
	Exponents map[string]int
	Calendars map[string]bool
}

func NewValidator(exponents map[string]int, calendars map[string]bool) (*Validator, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(planSchema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("urn:excommission:v1:plan", document); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile("urn:excommission:v1:plan")
	if err != nil {
		return nil, err
	}
	return &Validator{schema: schema, Exponents: exponents, Calendars: calendars}, nil
}
func (v *Validator) Decode(body []byte) (domain.Config, []byte, error) {
	if _, err := foundation.CanonicalJSON(body); err != nil {
		return domain.Config{}, nil, err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return domain.Config{}, nil, foundation.ErrValidation
	}
	if err = v.schema.Validate(instance); err != nil {
		return domain.Config{}, nil, fmt.Errorf("VALIDATION_FAILED: plan schema")
	}
	var config domain.Config
	if err = json.Unmarshal(body, &config); err != nil {
		return config, nil, foundation.ErrValidation
	}
	exponent, ok := v.Exponents[config.Currency]
	if !ok || !v.Calendars[config.CalendarVersion] {
		return config, nil, fmt.Errorf("UNSUPPORTED_CAPABILITY")
	}
	if err = domain.Validate(config, exponent); err != nil {
		return config, nil, err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return config, nil, err
	}
	canonical, err := foundation.CanonicalJSON(encoded)
	return config, canonical, err
}
