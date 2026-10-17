// Package domain defines the prototype's referral business independently of transport and storage.
package domain

import (
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"sort"
	"time"
)

var ErrValidation = foundation.ErrValidation

type Threshold struct {
	Deposit  string `json:"deposit_minor"`
	Turnover string `json:"valid_bet_minor"`
}
type Level struct {
	Number   int    `json:"level"`
	Members  int    `json:"qualified_members"`
	Turnover string `json:"daily_valid_bet_minor"`
	Rates    [3]int `json:"rates_ppm"`
}
type Step struct {
	Number  int    `json:"step"`
	Members int    `json:"additional_members"`
	Amount  string `json:"reward_minor"`
}
type Venue struct {
	ID      string `json:"venue_type_id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url"`
}
type Config struct {
	RiskRules         []RiskRule `json:"risk_rules,omitempty"`
	Enabled           bool       `json:"enabled"`
	Currency          string     `json:"currency"`
	CurrencyScale     int        `json:"currency_scale"`
	Timezone          string     `json:"timezone"`
	GradePeriod       string     `json:"grade_period"`
	GradeCondition    Threshold  `json:"grade_condition"`
	ReferralCondition Threshold  `json:"referral_condition"`
	InviteeCondition  Threshold  `json:"invitee_condition"`
	InviteeReward     string     `json:"invitee_reward_minor"`
	Levels            []Level    `json:"levels"`
	Steps             []Step     `json:"steps"`
	VenueIDs          []string   `json:"venue_type_ids"`
}

func Money(v string, positive bool) (*big.Int, error) {
	if len(v) == 0 || len(v) > 38 || (len(v) > 1 && v[0] == '0') {
		return nil, ErrValidation
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return nil, ErrValidation
		}
	}
	n, ok := new(big.Int).SetString(v, 10)
	if !ok || (positive && n.Sign() == 0) {
		return nil, ErrValidation
	}
	return n, nil
}
func (t Threshold) Validate() error {
	if _, e := Money(t.Deposit, true); e != nil {
		return e
	}
	_, e := Money(t.Turnover, true)
	return e
}
func (c *Config) Normalize() error {
	if len(c.RiskRules) > 30 {
		return ErrValidation
	}
	rules := map[string]bool{}
	for _, r := range c.RiskRules {
		if r.Validate() != nil || rules[r.ID] {
			return ErrValidation
		}
		rules[r.ID] = true
	}

	if !foundation.ValidID(c.Currency) || len(c.Currency) > 16 || c.CurrencyScale < 0 || c.CurrencyScale > 8 {
		return ErrValidation
	}
	if _, e := time.LoadLocation(c.Timezone); e != nil || c.Timezone == "Local" || c.Timezone == "" {
		return ErrValidation
	}
	switch c.GradePeriod {
	case "DAY", "WEEK", "MONTH", "LIFETIME":
	default:
		return ErrValidation
	}
	for _, t := range []Threshold{c.GradeCondition, c.ReferralCondition, c.InviteeCondition} {
		if e := t.Validate(); e != nil {
			return e
		}
	}
	if _, e := Money(c.InviteeReward, true); e != nil {
		return e
	}
	if len(c.Levels) == 0 || len(c.Levels) > 100 || len(c.Steps) == 0 || len(c.Steps) > 100 || len(c.VenueIDs) == 0 || len(c.VenueIDs) > 100 {
		return ErrValidation
	}
	lastMembers := -1
	lastBet := big.NewInt(-1)
	for i, v := range c.Levels {
		n, e := Money(v.Turnover, false)
		if e != nil || v.Number != i || v.Members < 0 || v.Members > 1000000000 || v.Members < lastMembers || n.Cmp(lastBet) < 0 {
			return ErrValidation
		}
		if i > 0 && v.Members == lastMembers && n.Cmp(lastBet) == 0 {
			return ErrValidation
		}
		for _, r := range v.Rates {
			if r < 0 || r > 1000000 {
				return ErrValidation
			}
		}
		lastMembers = v.Members
		lastBet = n
	}
	seen := map[int]bool{}
	total := 0
	for i, v := range c.Steps {
		if v.Number != i+1 || v.Members < 1 || v.Members > 1000000000 || seen[v.Members] {
			return ErrValidation
		}
		if _, e := Money(v.Amount, true); e != nil {
			return e
		}
		seen[v.Members] = true
		total += v.Members
		if total > 1000000000 {
			return ErrValidation
		}
	}
	ids := map[string]bool{}
	for _, id := range c.VenueIDs {
		if !foundation.ValidID(id) || ids[id] {
			return ErrValidation
		}
		ids[id] = true
	}
	sort.Strings(c.VenueIDs)
	return nil
}
func (c Config) ValidateChange(old Config) error {
	if c.GradePeriod != old.GradePeriod || c.Timezone != old.Timezone || c.Currency != old.Currency || c.CurrencyScale != old.CurrencyScale {
		return errors.New("IMMUTABLE_AFFILIATE_CONFIG")
	}
	// Existing reward identities cannot be re-used with different thresholds or amounts.
	if len(c.Steps) < len(old.Steps) {
		return errors.New("IMMUTABLE_REWARD_STEPS")
	}
	for i, v := range old.Steps {
		if c.Steps[i] != v {
			return errors.New("IMMUTABLE_REWARD_STEPS")
		}
	}
	return nil
}
func (c Config) Location() *time.Location {
	loc, e := time.LoadLocation(c.Timezone)
	if e != nil {
		return time.UTC
	}
	return loc
}
func (c Config) Day(at time.Time) (time.Time, time.Time) {
	v := at.In(c.Location())
	s := time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, c.Location())
	return s.UTC(), s.AddDate(0, 0, 1).UTC()
}
func (c Config) Period(at time.Time) (time.Time, time.Time) {
	start, end := c.Day(at)
	v := start.In(c.Location())
	switch c.GradePeriod {
	case "WEEK":
		offset := (int(v.Weekday()) + 6) % 7
		v = v.AddDate(0, 0, -offset)
		return v.UTC(), v.AddDate(0, 0, 7).UTC()
	case "MONTH":
		v = time.Date(v.Year(), v.Month(), 1, 0, 0, 0, 0, c.Location())
		return v.UTC(), v.AddDate(0, 1, 0).UTC()
	case "LIFETIME":
		return time.Time{}, end
	default:
		return start, end
	}
}

// DueDate returns the most recent complete business day eligible for the 01:00 run.
func (c Config) DueDate(now time.Time) time.Time {
	v := now.In(c.Location())
	today := time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, c.Location())
	if v.Hour() < 1 {
		today = today.AddDate(0, 0, -1)
	}
	return today.AddDate(0, 0, -1).UTC()
}
func (c Config) AllowsVenue(id string) bool {
	for _, v := range c.VenueIDs {
		if v == id {
			return true
		}
	}
	return false
}
