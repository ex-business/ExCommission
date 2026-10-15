package mysql

import (
	"bytes"
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
