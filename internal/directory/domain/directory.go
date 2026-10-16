package domain

import (
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"net/netip"
	"time"
)

type Statistics struct {
	UserID     string    `json:"user_id"`
	Currency   string    `json:"currency"`
	Observed   time.Time `json:"observed_at"`
	Deposit    string    `json:"deposit_minor"`
	Withdrawal string    `json:"withdrawal_minor"`
	Bet        string    `json:"bet_minor"`
	ValidBet   string    `json:"valid_bet_minor"`
	VIP        string    `json:"vip"`
	LoginIP    *string   `json:"login_ip"`
}

func (s *Statistics) Validate() error {
	if !foundation.ValidID(s.UserID) || !foundation.ValidID(s.Currency) || !foundation.ValidID(s.VIP) || s.Observed.IsZero() {
		return foundation.ErrValidation
	}
	for _, v := range []string{s.Deposit, s.Withdrawal, s.Bet, s.ValidBet} {
		n, ok := new(big.Int).SetString(v, 10)
		if !ok || n.Sign() < 0 || n.String() != v || len(v) > 38 {
			return foundation.ErrValidation
		}
	}
	bet, _ := new(big.Int).SetString(s.Bet, 10)
	valid, _ := new(big.Int).SetString(s.ValidBet, 10)
	if valid.Cmp(bet) > 0 {
		return foundation.ErrValidation
	}
	if s.LoginIP != nil {
		ip, e := netip.ParseAddr(*s.LoginIP)
		if e != nil || ip.Zone() != "" {
			return foundation.ErrValidation
		}
		v := ip.Unmap().String()
		s.LoginIP = &v
	}
	s.Observed = s.Observed.UTC().Truncate(time.Microsecond)
	return nil
}

type Row struct {
	UserID     string      `json:"user_id"`
	AgentID    *string     `json:"agent_id"`
	Name       string      `json:"display_name"`
	Depth      int         `json:"depth"`
	Status     string      `json:"status"`
	Statistics *Statistics `json:"statistics"`
}
type Summary struct {
	Members    int    `json:"members"`
	Missing    int    `json:"missing_statistics"`
	Complete   bool   `json:"complete"`
	Currency   string `json:"currency"`
	Deposit    string `json:"deposit_minor"`
	Withdrawal string `json:"withdrawal_minor"`
	Bet        string `json:"bet_minor"`
	ValidBet   string `json:"valid_bet_minor"`
}

func Aggregate(rows []Row, currency string) Summary {
	s := Summary{Members: len(rows), Currency: currency, Complete: true}
	totals := []*big.Int{new(big.Int), new(big.Int), new(big.Int), new(big.Int)}
	for _, r := range rows {
		if r.Statistics == nil {
			s.Missing++
			s.Complete = false
			continue
		}
		for j, v := range []string{r.Statistics.Deposit, r.Statistics.Withdrawal, r.Statistics.Bet, r.Statistics.ValidBet} {
			n, _ := new(big.Int).SetString(v, 10)
			totals[j].Add(totals[j], n)
		}
	}
	s.Deposit = totals[0].String()
	s.Withdrawal = totals[1].String()
	s.Bet = totals[2].String()
	s.ValidBet = totals[3].String()
	return s
}
