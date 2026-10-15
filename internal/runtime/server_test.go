package runtime

import (
	"net/http/httptest"
	"testing"
)

func TestOnlyImplementedHealthIsExposed(t *testing.T) {
	handler := Handler("api")
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/healthz", 200}, {"POST", "/healthz", 405}, {"POST", "/v1/payments", 404}} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, recorder.Code)
		}
	}
}
