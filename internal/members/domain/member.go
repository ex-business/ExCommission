package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

type Link struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

func ShareURL(base, code string) (string, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("UNSUPPORTED_CAPABILITY")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1")) {
		return "", errors.New("UNSUPPORTED_CAPABILITY")
	}
	q := u.Query()
	q.Set("invite", code)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type Earnings struct {
	Currency  string `json:"currency"`
	Target    string `json:"target_minor"`
	Pending   string `json:"pending_minor"`
	Available string `json:"available_minor"`
	Frozen    string `json:"frozen_minor"`
	Reserved  string `json:"reserved_minor"`
	Paid      string `json:"paid_minor"`
	Recovered string `json:"recovered_minor"`
	Debt      string `json:"debt_minor"`
}
type RewardCondition struct {
	Metric    string `json:"metric"`
	Unit      string `json:"unit"`
	Threshold string `json:"threshold"`
}
type Reward struct {
	ID         string            `json:"program_id"`
	Name       string            `json:"name"`
	Starts     time.Time         `json:"starts_at"`
	Ends       time.Time         `json:"ends_at"`
	Active     bool              `json:"active"`
	Conditions []RewardCondition `json:"conditions"`
	Qualified  int               `json:"qualified_members"`
	Triggered  int               `json:"triggered_members"`
}
type Rule struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Rules struct {
	Plan     string `json:"plan_name"`
	Currency string `json:"currency"`
	Minimum  string `json:"minimum_payout_minor"`
	Rules    []Rule `json:"rules"`
}

func FriendlyMetric(v string) string {
	switch strings.ToUpper(v) {
	case "CONFIRMED_DEPOSIT", "DEPOSIT", "DEPOSIT_AMOUNT", "DEPOSIT_TOTAL":
		return "累计充值"
	case "VALID_TURNOVER", "VALID_BET", "VALID_BET_AMOUNT", "VALID_BET_TOTAL":
		return "累计有效投注"
	case "BET", "BET_AMOUNT", "BET_TOTAL":
		return "累计投注"
	case "INVITED_COUNT", "REFERRAL_COUNT":
		return "邀请人数"
	default:
		return v
	}
}
