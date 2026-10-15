package mysql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	driver "github.com/go-sql-driver/mysql"
	"strings"

	"github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

var ErrConflict = domain.ErrConflict
var ErrValidation = domain.ErrValidation

type Scope = domain.Scope

func validID(value string) bool { return domain.ValidID(value) }

func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func CanonicalJSON(body []byte) ([]byte, error) { return domain.CanonicalJSON(body) }

func ValidKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

type Result = domain.CommandResult

// Command 在同一事务中持久化业务写入、outbox与HTTP结果；不确定故障不缓存为业务失败。
func (s *Store) Command(ctx context.Context, scope Scope, operation, key string, canonical []byte, fn func(ports.Executor) (Result, error)) (Result, error) {
	if !scope.Valid() || !validID(operation) || !ValidKey(key) {
		return Result{}, ErrValidation
	}
	normalized, err := CanonicalJSON(canonical)
	if err != nil || !bytes.Equal(normalized, canonical) {
		return Result{}, ErrValidation
	}
	var result Result
	sum := sha256.Sum256(canonical)
	err = s.Within(ctx, func(tx ports.Executor) error {
		// 无副作用的重复键更新取得行锁；并发请求等待首个事务完成。
		_, err := tx.ExecContext(ctx, `INSERT INTO command_results(tenant_id,app_id,operation,idempotency_key,canonical_body,body_hash,created_at)
   VALUES (?,?,?,?,?,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE idempotency_key=idempotency_key`, scope.Tenant, scope.App, operation, key, canonical, sum[:])
		if err != nil {
			return err
		}
		var stored, storedHash, body []byte
		var status *int
		var resource *string
		err = tx.QueryRowContext(ctx, `SELECT canonical_body,body_hash,http_status,result_body,resource_id FROM command_results
   WHERE tenant_id=? AND app_id=? AND operation=? AND idempotency_key=? FOR UPDATE`, scope.Tenant, scope.App, operation, key).Scan(&stored, &storedHash, &status, &body, &resource)
		if err != nil {
			return err
		}
		if !bytes.Equal(stored, canonical) || !bytes.Equal(storedHash, sum[:]) {
			return ErrConflict
		}
		if status != nil {
			result = Result{Status: *status, Body: body, Replayed: true}
			if resource != nil {
				result.ResourceID = *resource
			}
			return nil
		}
		result, err = fn(tx)
		if err != nil {
			return err
		}
		if result.Status < 200 || result.Status >= 500 || len(result.Body) == 0 || len(strings.TrimSpace(result.ResourceID)) == 0 {
			return errors.New("invalid deterministic command result")
		}
		_, err = tx.ExecContext(ctx, `UPDATE command_results SET http_status=?,result_body=?,resource_id=?
   WHERE tenant_id=? AND app_id=? AND operation=? AND idempotency_key=?`, result.Status, result.Body, result.ResourceID, scope.Tenant, scope.App, operation, key)
		return err
	})
	return result, err
}

func (s *Store) Execute(ctx context.Context, scope Scope, operation, key string, input any, fn func(ports.Executor) (domain.CommandResult, error)) (domain.CommandResult, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return Result{}, ErrValidation
	}
	canonical, err := CanonicalJSON(body)
	if err != nil {
		return Result{}, err
	}
	return s.Command(ctx, scope, operation, key, canonical, func(tx ports.Executor) (Result, error) {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT command_business"); err != nil {
			return Result{}, err
		}
		result, err := fn(tx)
		if err == nil {
			return result, nil
		}
		code := strings.Split(err.Error(), ":")[0]
		status := 0
		switch code {
		case "VALIDATION_FAILED", "UNIT_MISMATCH":
			status = 400
		case "PERMISSION_DENIED", "SELF_APPROVAL_FORBIDDEN":
			status = 403
		case "NOT_FOUND", "USER_NOT_FOUND":
			status = 404
		case "IDEMPOTENCY_CONFLICT", "REVISION_CONFLICT", "STALE_APPROVAL", "STATE_CONFLICT":
			status = 409
		case "RELATION_CYCLE", "RELATION_HISTORY_MISSING", "UNSUPPORTED_CAPABILITY", "UNKNOWN_FACT", "NO_EFFECTIVE_PLAN_VERSION", "RATE_MISSING":
			status = 422
		}
		var sqlError *driver.MySQLError
		if errors.As(err, &sqlError) && sqlError.Number == 1062 {
			status = 409
			code = "STATE_CONFLICT"
		}
		if status == 0 {
			return Result{}, err
		}
		if _, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT command_business"); err != nil {
			return Result{}, err
		}
		metadata := domain.RequestFrom(ctx)
		if metadata.RequestID == "" {
			metadata.RequestID = "internal"
		}
		body, err := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": code, "retryable": false, "resource_ref": nil, "details": map[string]string{}}, "request_id": metadata.RequestID})
		return Result{Status: status, Body: body, ResourceID: "cmd_" + digest(canonical)[:32]}, err
	})
}

func (s *Store) Lookup(ctx context.Context, scope Scope, operation, key string, input any) (Result, bool, error) {
	if !scope.Valid() || !ValidKey(key) {
		return Result{}, false, ErrValidation
	}
	body, err := json.Marshal(input)
	if err != nil {
		return Result{}, false, ErrValidation
	}
	canonical, err := CanonicalJSON(body)
	if err != nil {
		return Result{}, false, err
	}
	var old, resultBody []byte
	var status *int
	var resource *string
	err = s.DB.QueryRowContext(ctx, `SELECT canonical_body,http_status,result_body,resource_id FROM command_results WHERE tenant_id=? AND app_id=? AND operation=? AND idempotency_key=?`, scope.Tenant, scope.App, operation, key).Scan(&old, &status, &resultBody, &resource)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	if !bytes.Equal(old, canonical) {
		return Result{}, false, ErrConflict
	}
	if status == nil {
		return Result{}, false, nil
	}
	result := Result{Status: *status, Body: resultBody, Replayed: true}
	if resource != nil {
		result.ResourceID = *resource
	}
	return result, true, nil
}
