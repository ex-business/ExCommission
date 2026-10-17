package domain

import (
	"testing"
	"time"
)

func TestStatisticsRequireExactNonnegativeMoneyAndIP(t *testing.T) {
	ip := "::ffff:192.0.2.1"
	s := Statistics{UserID: "u", Currency: "USD", Observed: time.Now(), Deposit: "0", Withdrawal: "1", Bet: "2", ValidBet: "2", VIP: "VIP0", LoginIP: &ip}
	if e := s.Validate(); e != nil || *s.LoginIP != "192.0.2.1" {
		t.Fatal(s, e)
	}
	for _, v := range []string{"-1", "01", "1.2", "100000000000000000000000000000000000000"} {
		bad := s
		bad.Deposit = v
		if bad.Validate() == nil {
			t.Fatal("invalid money", v)
		}
	}
	bad := s
	bad.ValidBet = "3"
	if bad.Validate() == nil {
		t.Fatal("valid wager exceeds turnover")
	}
	summary := Aggregate([]Row{{Statistics: &s}, {Statistics: nil}}, "USD")
	if summary.Complete || summary.Missing != 1 || summary.Withdrawal != "1" {
		t.Fatal(summary)
	}
}
