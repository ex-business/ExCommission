package domain

import (
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"net"
	"time"
)

type RecordFilter struct {
	BusinessDay    string
	RewardID       string
	ClaimID        string
	ClaimIP        string
	Sort           string
	TimeField      string
	Snapshot       uint64
	Kind           string
	Status         string
	UserID         string
	From, To, AsOf time.Time
	After          string
	Limit          int
}

func (f RecordFilter) Validate(now time.Time) error {
	if f.BusinessDay != "" {
		if _, e := time.Parse("2006-01-02", f.BusinessDay); e != nil {
			return foundation.ErrValidation
		}
	}
	if len(f.RewardID) > 128 || len(f.ClaimID) > 128 || len(f.After) > 512 || len(f.UserID) > 128 {
		return foundation.ErrValidation
	}
	if f.ClaimIP != "" && net.ParseIP(f.ClaimIP) == nil {
		return foundation.ErrValidation
	}
	switch f.Sort {
	case "", "amount_desc", "time_desc":
	default:
		return foundation.ErrValidation
	}
	switch f.TimeField {
	case "", "unlocked_at", "claimed_at":
	default:
		return foundation.ErrValidation
	}

	if f.Kind != "" && f.Kind != Referral && f.Kind != Invitee && f.Kind != Commission {
		return foundation.ErrValidation
	}
	switch f.Status {
	case "", "PENDING_REVIEW", "CLAIMABLE", "CLAIMING", "CLAIMED", "CANCELLED":
	default:
		return foundation.ErrValidation
	}
	if f.From.IsZero() || !f.From.Before(f.To) || f.To.After(f.From.AddDate(1, 0, 0)) || f.AsOf.After(now) || f.AsOf.IsZero() || f.Limit < 1 || f.Limit > 100 {
		return foundation.ErrValidation
	}
	return nil
}

type RewardSummary struct {
	Currency  string `json:"currency"`
	Count     int    `json:"count"`
	Total     string `json:"total_minor"`
	Claimable string `json:"claimable_minor"`
	Claimed   string `json:"claimed_minor"`
	Pending   string `json:"pending_review_minor"`
	Cancelled string `json:"cancelled_minor"`
}
type RecordPage struct {
	Snapshot    uint64          `json:"snapshot_revision,string"`
	Rows        []Reward        `json:"rows"`
	Summary     []RewardSummary `json:"summary"`
	PageSummary []RewardSummary `json:"page_summary"`
	Next        *string         `json:"next_after_id"`
	AsOf        time.Time       `json:"as_of"`
}

func Summarize(rows []Reward) []RewardSummary {
	out := []RewardSummary{}
	index := map[string]int{}
	add := func(a, b string) string {
		x, _ := new(big.Int).SetString(a, 10)
		y, _ := new(big.Int).SetString(b, 10)
		return x.Add(x, y).String()
	}
	for _, r := range rows {
		n, ok := index[r.Currency]
		if !ok {
			n = len(out)
			index[r.Currency] = n
			out = append(out, RewardSummary{Currency: r.Currency, Total: "0", Claimable: "0", Claimed: "0", Pending: "0", Cancelled: "0"})
		}
		v := &out[n]
		v.Count++
		v.Total = add(v.Total, r.Amount)
		switch r.Status {
		case "CLAIMABLE":
			v.Claimable = add(v.Claimable, r.Amount)
		case "CLAIMED":
			v.Claimed = add(v.Claimed, r.Amount)
		case "PENDING_REVIEW", "CLAIMING":
			v.Pending = add(v.Pending, r.Amount)
		case "CANCELLED":
			v.Cancelled = add(v.Cancelled, r.Amount)
		}
	}
	return out
}
