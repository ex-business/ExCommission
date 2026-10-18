package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"time"
)

type Reversal struct {
	Venue            string    `json:"venue_type_id,omitempty"`
	Source           string    `json:"source_id"`
	EventID          string    `json:"event_id"`
	BusinessID       string    `json:"business_id"`
	OriginalID       string    `json:"original_event_id"`
	OriginalBusiness string    `json:"original_business_id"`
	ExternalUser     string    `json:"external_user_id"`
	Kind             string    `json:"kind"`
	Currency         string    `json:"currency"`
	Amount           string    `json:"amount_minor"`
	ValidBet         string    `json:"valid_bet_minor"`
	At               time.Time `json:"business_time"`
	Reason           string    `json:"reason"`
}

func (r Reversal) OriginalKind() string {
	if r.Kind == "DEPOSIT_REFUNDED" {
		return "DEPOSIT_SUCCEEDED"
	}
	if r.Kind == "BET_CANCELLED" {
		return "BET_SETTLED"
	}
	return ""
}
func (r Reversal) Validate(now time.Time) error {
	for _, v := range []string{r.Source, r.EventID, r.BusinessID, r.OriginalID, r.OriginalBusiness, r.ExternalUser, r.Reason} {
		if !foundation.ValidID(v) {
			return foundation.ErrValidation
		}
	}
	a, ok := new(big.Int).SetString(r.Amount, 10)
	b, bok := new(big.Int).SetString(r.ValidBet, 10)
	if !ok || !bok || a.Sign() <= 0 || b.Sign() < 0 || a.String() != r.Amount || b.String() != r.ValidBet || len(r.Amount) > 38 || len(r.ValidBet) > 38 || b.Cmp(a) > 0 || r.OriginalKind() == "" || r.Currency == "" || r.Venue != "" || r.At.Year() < 1000 || r.At.After(now) || !r.At.Equal(r.At.Truncate(time.Microsecond)) {
		return foundation.ErrValidation
	}
	if r.Kind == "DEPOSIT_REFUNDED" && r.ValidBet != "0" {
		return foundation.ErrValidation
	}
	return nil
}
func (r Reversal) Hash() string {
	r.EventID = ""
	b, _ := json.Marshal(r)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type ReversalAdjustment struct {
	Source           string    `json:"source_id"`
	Business         string    `json:"business_id"`
	OriginalID       string    `json:"original_event_id"`
	OriginalBusiness string    `json:"original_business_id"`
	Kind             string    `json:"kind"`
	ID               string    `json:"adjustment_id"`
	ReversalID       string    `json:"reversal_id"`
	RewardID         string    `json:"reward_id"`
	UserID           string    `json:"user_id"`
	Currency         string    `json:"currency"`
	Before           string    `json:"before_target"`
	After            string    `json:"after_target"`
	Debt             string    `json:"debt_added"`
	Status           string    `json:"status"`
	At               time.Time `json:"created_at"`
}
