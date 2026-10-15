package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
)

// SandboxEvidence is an explicit local bank-confirmation fixture, never a real bank assertion.
type SandboxEvidence struct{ File string }

func (p SandboxEvidence) Verify(ctx context.Context, s foundation.Scope, reference, agent, currency, amount string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.File == "" {
		return "", errors.New("UNSUPPORTED_CAPABILITY")
	}
	body, err := os.ReadFile(p.File)
	if err != nil {
		return "", err
	}
	var proofs map[string]struct {
		Tenant    string `json:"tenant_id"`
		App       string `json:"app_id"`
		Agent     string `json:"agent_id"`
		Currency  string `json:"currency"`
		Amount    string `json:"minor_units"`
		Confirmed bool   `json:"confirmed"`
	}
	if err = json.Unmarshal(body, &proofs); err != nil {
		return "", err
	}
	v, ok := proofs[reference]
	if !ok || !v.Confirmed || v.Tenant != s.Tenant || v.App != s.App || v.Agent != agent || v.Currency != currency || v.Amount != amount {
		return "", errors.New("EVIDENCE_NOT_CONFIRMED")
	}
	proof, _ := json.Marshal(v)
	hash := sha256.Sum256(proof)
	return hex.EncodeToString(hash[:]), nil
}
