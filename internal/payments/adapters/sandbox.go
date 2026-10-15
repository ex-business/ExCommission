package adapters

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
)

// Sandbox never asserts real bank delivery; explicit signed fixtures supply terminal outcomes.
type Sandbox struct {
	Key         []byte
	ResultsFile string
	Disabled    bool
}

func (s Sandbox) Enabled() bool { return !s.Disabled && len(s.Key) >= 32 }
func (s Sandbox) Submit(ctx context.Context, p model.Payment) (model.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return model.Outcome{}, err
	}
	return model.Outcome{MerchantID: p.MerchantID, Currency: p.Currency, Amount: p.Amount, Status: "UNKNOWN"}, nil
}
func (s Sandbox) Query(ctx context.Context, p model.Payment) (model.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return model.Outcome{}, err
	}
	data, err := s.read()
	if err != nil {
		return model.Outcome{}, err
	}
	if result, ok := data.Queries[p.MerchantID]; ok {
		return result, nil
	}
	return model.Outcome{MerchantID: p.MerchantID, Currency: p.Currency, Amount: p.Amount, Status: "UNKNOWN"}, nil
}

type sandboxResults struct {
	Queries         map[string]model.Outcome   `json:"queries"`
	Reconciliations map[string][]model.Outcome `json:"reconciliations"`
}

func (s Sandbox) read() (sandboxResults, error) {
	var results sandboxResults
	if s.ResultsFile == "" {
		return results, nil
	}
	body, err := os.ReadFile(s.ResultsFile)
	if err != nil {
		return results, err
	}
	err = json.Unmarshal(body, &results)
	return results, err
}
func (s Sandbox) Reconcile(ctx context.Context, ref string) ([]model.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := s.read()
	if err != nil {
		return nil, err
	}
	rows, ok := data.Reconciliations[ref]
	if !ok {
		return nil, errors.New("NOT_FOUND")
	}
	return rows, nil
}
func (s Sandbox) VerifyCallback(raw []byte, signature string) (model.Outcome, error) {
	var result model.Outcome
	if len(s.Key) < 32 {
		return result, errors.New("UNSUPPORTED_CAPABILITY")
	}
	sig, err := hex.DecodeString(signature)
	mac := hmac.New(sha256.New, s.Key)
	mac.Write(raw)
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return result, errors.New("SOURCE_SIGNATURE_INVALID")
	}
	if _, err = foundation.CanonicalJSON(raw); err != nil {
		return result, foundation.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return result, foundation.ErrValidation
	}
	if !foundation.ValidID(result.CallbackID) || !foundation.ValidID(result.MerchantID) {
		return result, foundation.ErrValidation
	}
	return result, nil
}
