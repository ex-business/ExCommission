package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictCommandFields(t *testing.T) {
	type Command struct {
		ID       string  `json:"id"`
		Revision uint64  `json:"expected_revision"`
		Parent   *string `json:"parent"`
	}
	for _, body := range []string{`{}`, `{"id":"a","expected_revision":0}`, `{"id":"a","id":"b","expected_revision":0,"parent":null}`, `{"id":"a","expected_revision":null,"parent":null}`, `{"id":"a","expected_revision":0,"parent":null,"extra":1}`, `{"id":"a","expected_revision":0,"parent":null} {}`} {
		var command Command
		if err := Decode(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), &command); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	var command Command
	if err := Decode(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"a","expected_revision":0,"parent":null}`)), &command); err != nil {
		t.Fatal(err)
	}
}
func TestCursorScopeAndTampering(t *testing.T) {
	codec := CursorCodec{Key: []byte(strings.Repeat("a", 32))}
	value := Cursor{Tenant: "t", App: "a", Root: "r", At: "2026-10-01T00:00:00Z", Limit: 2, Offset: 2}
	token, err := codec.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := codec.Decode(token, value); err != nil || decoded.Offset != 2 {
		t.Fatal("valid cursor", err)
	}
	for _, changed := range []Cursor{{Tenant: "other", App: "a", Root: "r", At: value.At, Limit: 2}, {Tenant: "t", App: "a", Root: "r2", At: value.At, Limit: 2}, {Tenant: "t", App: "a", Root: "r", At: value.At, Limit: 3}} {
		if _, err = codec.Decode(token, changed); err == nil {
			t.Fatal("unbound cursor")
		}
	}
	if _, err = codec.Decode(token+"x", value); err == nil {
		t.Fatal("tampered cursor")
	}
}
