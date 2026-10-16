// Package domain 定义有效会员门槛与可信业务事实，不执行充值、投注或发奖。
package domain

import (
	"errors"
	"math/big"
	"regexp"
	"sort"
	"time"
)

var ErrValidation = errors.New("VALIDATION_FAILED")
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,37})$`)

func Amount(value string) (*big.Int, error) {
	if !amountPattern.MatchString(value) {
		return nil, ErrValidation
	}
	n, _ := new(big.Int).SetString(value, 10)
	return n, nil
}

type Policy struct {
	ID       string   `json:"policy_id"`
	Source   string   `json:"source_id"`
	Currency string   `json:"currency"`
	Deposit  string   `json:"deposit_threshold_minor"`
	Bet      string   `json:"valid_bet_threshold_minor"`
	Venues   []string `json:"venue_types"`
}

// ValidatePolicy 门槛和场馆集合一经发布不可覆盖；后续规则使用新的policy_id保留历史口径。
func ValidatePolicy(p Policy) error {
	d, err := Amount(p.Deposit)
	if err != nil || d.Sign() <= 0 {
		return ErrValidation
	}
	b, err := Amount(p.Bet)
	if err != nil || b.Sign() <= 0 || len(p.Venues) == 0 || len(p.Venues) > 64 {
		return ErrValidation
	}
	seen := map[string]bool{}
	for _, v := range p.Venues {
		if v == "" || len(v) > 128 || seen[v] {
			return ErrValidation
		}
		seen[v] = true
	}
	return nil
}

type Fact struct {
	Schema       string    `json:"schema_version"`
	PolicyID     string    `json:"policy_id"`
	Source       string    `json:"source_id"`
	EventID      string    `json:"event_id"`
	BusinessID   string    `json:"business_id"`
	Type         string    `json:"event_type"`
	ExternalUser string    `json:"external_user_id"`
	At           time.Time `json:"business_time"`
	Amount       string    `json:"amount_minor"`
	Currency     string    `json:"currency"`
	Venue        *string   `json:"venue_type"`
}

type Member struct {
	UserID, AgentID string
	ParentID        *string
	RegisteredAt    time.Time
}

type Status struct {
	PolicyID         string     `json:"policy_id"`
	Source           string     `json:"source_id"`
	ExternalUser     string     `json:"external_user_id"`
	UserID           string     `json:"user_id"`
	AgentID          string     `json:"agent_id"`
	ParentID         *string    `json:"parent_agent_id"`
	Currency         string     `json:"currency"`
	DepositTotal     string     `json:"deposit_total_minor"`
	BetTotal         string     `json:"valid_bet_total_minor"`
	DepositThreshold string     `json:"deposit_threshold_minor"`
	BetThreshold     string     `json:"valid_bet_threshold_minor"`
	Effective        bool       `json:"is_effective"`
	QualifiedAt      *time.Time `json:"qualified_at"`
	FirstObservedAt  *time.Time `json:"first_observed_at"`
	Revision         uint64     `json:"revision"`
}

// Evaluate 仅按成功事实的业务时间累计，不限达标时长，不将金额转浮点或混币相加。
// 迟到事实可能使真实达标时点前移；首次观测时点由应用层单独保留，不能冒充业务达标时间。
func Evaluate(p Policy, member Member, facts []Fact) (Status, error) {
	if err := ValidatePolicy(p); err != nil {
		return Status{}, err
	}
	state := Status{PolicyID: p.ID, Source: p.Source, UserID: member.UserID, AgentID: member.AgentID, ParentID: member.ParentID, Currency: p.Currency, DepositTotal: "0", BetTotal: "0", DepositThreshold: p.Deposit, BetThreshold: p.Bet}
	deposit, bet := new(big.Int), new(big.Int)
	d, _ := Amount(p.Deposit)
	b, _ := Amount(p.Bet)
	ordered := append([]Fact(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].EventID < ordered[j].EventID
		}
		return ordered[i].At.Before(ordered[j].At)
	})
	venues := map[string]bool{}
	for _, v := range p.Venues {
		venues[v] = true
	}
	for _, f := range ordered {
		if f.PolicyID != p.ID || f.Source != p.Source || f.Currency != p.Currency || f.At.Before(member.RegisteredAt) {
			return Status{}, ErrValidation
		}
		n, err := Amount(f.Amount)
		if err != nil {
			return Status{}, err
		}
		switch f.Type {
		case "DEPOSIT_SUCCEEDED":
			if n.Sign() <= 0 || f.Venue != nil {
				return Status{}, ErrValidation
			}
			deposit.Add(deposit, n)
		case "BET_SETTLED":
			if f.Venue == nil || !venues[*f.Venue] {
				return Status{}, ErrValidation
			}
			bet.Add(bet, n)
		default:
			return Status{}, ErrValidation
		}
		if state.QualifiedAt == nil && member.ParentID != nil && deposit.Cmp(d) >= 0 && bet.Cmp(b) >= 0 {
			at := f.At.UTC()
			state.QualifiedAt = &at
			state.Effective = true
		}
	}
	state.DepositTotal = deposit.String()
	state.BetTotal = bet.String()
	return state, nil
}
