package adapters

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/integration/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/event.schema.json
var eventSchema []byte

// Source校验原始字节签名，规范化后只把受信确认事实交给接收事务。
type Source struct {
	Key    []byte
	ID     string
	schema *jsonschema.Schema
}

func NewSource(id string, key []byte) (*Source, error) {
	if id == "" || len(key) < 32 {
		return nil, foundation.ErrValidation
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(eventSchema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("urn:excommission:v1:event", document); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile("urn:excommission:v1:event")
	return &Source{ID: id, Key: append([]byte(nil), key...), schema: schema}, err
}
func (s *Source) Normalize(raw []byte, signature string) (domain.Event, error) {
	mac := hmac.New(sha256.New, s.Key)
	mac.Write(raw)
	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(mac.Sum(nil), provided) {
		return domain.Event{}, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	if _, err = foundation.CanonicalJSON(raw); err != nil {
		return domain.Event{}, err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return domain.Event{}, foundation.ErrValidation
	}
	if err = s.schema.Validate(instance); err != nil {
		return domain.Event{}, foundation.ErrValidation
	}
	var event domain.Event
	if err = json.Unmarshal(raw, &event); err != nil {
		return event, foundation.ErrValidation
	}
	if event.Source != s.ID {
		return event, errors.New("PERMISSION_DENIED")
	}
	basis, err := foundation.ParseDecimal(event.Payload.Basis)
	if err != nil {
		return event, err
	}
	event.Payload.Basis = basis.String()
	if event.Payload.Quantity != "" {
		quantity, err := foundation.ParseDecimal(event.Payload.Quantity)
		if err != nil {
			return event, err
		}
		event.Payload.Quantity = quantity.String()
	}
	yes := true
	event.Payload.Confirmed = &yes
	event.Time = event.Time.UTC()
	return event, nil
}
