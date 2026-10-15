package runtime

import (
	"context"
	"errors"
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

func TestReadinessReportsDependencyFailure(t *testing.T) {
	for _, tc := range []struct {
		ready  func(context.Context) error
		status int
	}{{nil, 503}, {func(context.Context) error { return errors.New("offline") }, 503}, {func(context.Context) error { return nil }, 200}} {
		recorder := httptest.NewRecorder()
		handler("api", tc.ready).ServeHTTP(recorder, httptest.NewRequest("GET", "/readyz", nil))
		if recorder.Code != tc.status {
			t.Fatalf("readiness %d", recorder.Code)
		}
	}
}
