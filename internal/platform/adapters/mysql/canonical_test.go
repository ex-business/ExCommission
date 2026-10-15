package mysql

import (
	"bytes"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"testing"
)

func TestCanonicalJSONAndKey(t *testing.T) {
	a, err := CanonicalJSON([]byte(` { "b": ["2","1"], "a":"1" } `))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CanonicalJSON([]byte(`{"a":"1","b":["2","1"]}`))
	if !bytes.Equal(a, b) {
		t.Fatal("object order")
	}
	c, _ := CanonicalJSON([]byte(`{"a":"1","b":["1","2"]}`))
	if bytes.Equal(a, c) {
		t.Fatal("array order changed")
	}
	for _, input := range []string{"null", "[]", "{} {}", "{bad}", `{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`} {
		if _, err := CanonicalJSON([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, key := range []string{"", "bad\n", "中文"} {
		if ValidKey(key) {
			t.Fatal("invalid key accepted")
		}
	}
	if !ValidKey("valid key-1") {
		t.Fatal("valid key rejected")
	}
}

func TestBusinessCanonicalProjection(t *testing.T) {
	first, err := foundation.BusinessCanonicalJSON([]byte(`{"basis_amount":"1.0","business_time":"2026-10-01T08:00:00+08:00","rate_ppm":"000001","request_id":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := foundation.BusinessCanonicalJSON([]byte(`{"basis_amount":"1.00000000","business_time":"2026-10-01T00:00:00Z","rate_ppm":"1","request_id":"two"}`))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("semantic canonical mismatch %s %s %v", first, second, err)
	}
	if string(first) != `{"basis_amount":"100000000","business_time":"2026-10-01T00:00:00.000000Z","rate_ppm":"1"}` {
		t.Fatalf("not atoms projection: %s", first)
	}
}
