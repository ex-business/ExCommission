package mysql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

var ErrConflict = domain.ErrConflict
var ErrValidation = domain.ErrValidation

type Scope = domain.Scope

func validID(value string) bool { return domain.ValidID(value) }

func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// CanonicalJSON 只处理JSON结构；调用方必须先按严格schema校验并规范化默认值、时间及金额。
func CanonicalJSON(body []byte) ([]byte, error) {
	check := json.NewDecoder(bytes.NewReader(body))
	check.UseNumber()
	if err := rejectDuplicateKeys(check); err != nil {
		return nil, ErrValidation
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, ErrValidation
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrValidation
	}
	return json.Marshal(object)
}

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

type Result struct {
	Status     int
	Body       []byte
	ResourceID string
	Replayed   bool
}

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

func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrValidation
			}
			seen[name] = true
			if err = rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err = rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return ErrValidation
	}
	_, err = decoder.Token()
	return err
}
