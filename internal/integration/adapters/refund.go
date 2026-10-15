package adapters

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/refund.schema.json
var refundSchema []byte

func (s *Source) NormalizeRefund(raw []byte, signature string) (model.Refund, error) {
	var result model.Refund
	mac := hmac.New(sha256.New, s.Key)
	mac.Write(raw)
	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(mac.Sum(nil), provided) {
		return result, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	if _, err = foundation.CanonicalJSON(raw); err != nil {
		return result, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(refundSchema))
	if err != nil {
		return result, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("urn:excommission:v1:refund", document); err != nil {
		return result, err
	}
	schema, err := compiler.Compile("urn:excommission:v1:refund")
	if err != nil {
		return result, err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return result, foundation.ErrValidation
	}
	if err = schema.Validate(instance); err != nil {
		return result, foundation.ErrValidation
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, foundation.ErrValidation
	}
	if result.Source != s.ID {
		return result, errors.New("PERMISSION_DENIED")
	}
	return result, nil
}
