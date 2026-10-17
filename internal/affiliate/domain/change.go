package domain

import (
	"bytes"
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"reflect"
	"time"
)

type Version struct {
	Revision   uint64    `json:"revision"`
	Config     Config    `json:"config"`
	Effective  time.Time `json:"effective_from"`
	ApprovalID string    `json:"approval_id"`
}
type Configuration struct {
	Configured bool     `json:"configured"`
	Revision   uint64   `json:"revision"`
	Current    *Version `json:"current"`
	Scheduled  *Version `json:"scheduled"`
}
type Change struct {
	Config   Config `json:"config"`
	Revision uint64 `json:"expected_revision"`
	Reason   string `json:"reason"`
}

// Require explicit enabled=false and revision=0 rather than silently supplying omitted fields.
func (v *Change) UnmarshalJSON(body []byte) error {
	type plain Change
	var raw struct {
		Config   *json.RawMessage `json:"config"`
		Revision *uint64          `json:"expected_revision"`
		Reason   *string          `json:"reason"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if e := d.Decode(&raw); e != nil {
		return ErrValidation
	}
	if raw.Config == nil || raw.Revision == nil || raw.Reason == nil {
		return ErrValidation
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(*raw.Config, &fields) != nil || fields["enabled"] == nil || fields["currency_scale"] == nil {
		return ErrValidation
	}
	var enabled *bool
	var scale *int
	if json.Unmarshal(fields["enabled"], &enabled) != nil || enabled == nil || json.Unmarshal(fields["currency_scale"], &scale) != nil || scale == nil {
		return ErrValidation
	}
	if !completeJSON(*raw.Config, reflect.TypeOf(Config{})) {
		return ErrValidation
	}
	dec := json.NewDecoder(bytes.NewReader(*raw.Config))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&v.Config); e != nil {
		return ErrValidation
	}
	v.Revision = *raw.Revision
	v.Reason = *raw.Reason
	return nil
}
func (v *Change) Normalize() error {
	if !foundation.ValidID(v.Reason) {
		return ErrValidation
	}
	return v.Config.Normalize()
}

// completeJSON rejects omitted/null nested values, including meaningful zero-valued ranks/rates.
func completeJSON(raw json.RawMessage, typ reflect.Type) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := f.Tag.Get("json")
			v, ok := fields[key]
			if !ok || !completeJSON(v, f.Type) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		if typ.Kind() == reflect.Array && len(values) != typ.Len() {
			return false
		}
		for _, v := range values {
			if !completeJSON(v, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
