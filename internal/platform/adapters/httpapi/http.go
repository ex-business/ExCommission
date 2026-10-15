package httpapi

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/ex-business/ExCommission/internal/platform/domain"
	driver "github.com/go-sql-driver/mysql"
)

// Decode 要求顶层命令的每个声明字段都显式提供；可空字段仍须传null。
func Decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return domain.ErrValidation
	}
	if _, err = domain.CanonicalJSON(body); err != nil {
		return domain.ErrValidation
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(body, &fields); err != nil || fields == nil {
		return domain.ErrValidation
	}
	value := reflect.TypeOf(target)
	if value.Kind() != reflect.Pointer || value.Elem().Kind() != reflect.Struct {
		return domain.ErrValidation
	}
	for i := 0; i < value.Elem().NumField(); i++ {
		field := value.Elem().Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "-" && name != "" {
			raw, ok := fields[name]
			if !ok {
				return domain.ErrValidation
			}
			if string(raw) == "null" && field.Type.Kind() != reflect.Pointer {
				return domain.ErrValidation
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return domain.ErrValidation
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.ErrValidation
	}
	return nil
}
func Write(w http.ResponseWriter, result domain.CommandResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}
func Error(w http.ResponseWriter, request string, err error) {
	status, code, retry := 503, "DEPENDENCY_UNAVAILABLE", true
	var sqlError *driver.MySQLError
	if errors.Is(err, sql.ErrNoRows) {
		status, code, retry = 404, "NOT_FOUND", false
	} else if errors.As(err, &sqlError) && sqlError.Number == 1062 {
		status, code, retry = 409, "STATE_CONFLICT", false
	} else {
		switch strings.Split(err.Error(), ":")[0] {
		case "VALIDATION_FAILED", "UNIT_MISMATCH":
			status, code, retry = 400, "VALIDATION_FAILED", false
		case "PERMISSION_DENIED", "SELF_APPROVAL_FORBIDDEN":
			status, code, retry = 403, strings.Split(err.Error(), ":")[0], false
		case "NOT_FOUND", "USER_NOT_FOUND":
			status, code, retry = 404, "NOT_FOUND", false
		case "IDEMPOTENCY_CONFLICT", "REVISION_CONFLICT", "STALE_APPROVAL", "STATE_CONFLICT":
			status, code, retry = 409, strings.Split(err.Error(), ":")[0], false
		case "RELATION_CYCLE", "RELATION_HISTORY_MISSING", "UNSUPPORTED_CAPABILITY":
			status, code, retry = 422, strings.Split(err.Error(), ":")[0], false
		}
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": code, "retryable": retry, "resource_ref": nil, "details": map[string]string{}}, "request_id": request})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
