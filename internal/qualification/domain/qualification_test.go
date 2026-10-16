package domain

import (
	"testing"
	"time"
)

func TestQualificationBothThresholdsAndLateFacts(t *testing.T) {
	parent := "parent"
	member := Member{UserID: "u", AgentID: "a", ParentID: &parent, RegisteredAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	p := Policy{ID: "p", Source: "sandbox", Currency: "USD", Deposit: "5000", Bet: "10000", Venues: []string{"SPORTS"}}
	fact := func(id, kind, amount string, day int) Fact {
		f := Fact{PolicyID: "p", Source: "sandbox", EventID: id, ExternalUser: "member", Currency: "USD", Amount: amount, Type: kind, At: member.RegisteredAt.AddDate(0, 0, day)}
		if kind == "BET_SETTLED" {
			v := "SPORTS"
			f.Venue = &v
		}
		return f
	}
	deposit := fact("deposit", "DEPOSIT_SUCCEEDED", "5000", 3)
	bet := fact("bet", "BET_SETTLED", "10000", 4)
	for _, facts := range [][]Fact{{deposit}, {bet}, {fact("small-deposit", "DEPOSIT_SUCCEEDED", "4999", 3), bet}} {
		state, err := Evaluate(p, member, facts)
		if err != nil || state.Effective || state.QualifiedAt != nil {
			t.Fatal(state, err)
		}
	}
	state, err := Evaluate(p, member, []Fact{bet, deposit})
	if err != nil || !state.Effective || !state.QualifiedAt.Equal(bet.At) {
		t.Fatal(state, err)
	}
	// 晚到的业务事实可校正真实达标时间，算法结果不依赖到达顺序或系统当前时间。
	earlyBet := fact("early", "BET_SETTLED", "10000", 1)
	state, err = Evaluate(p, member, []Fact{bet, deposit, earlyBet})
	if err != nil || !state.QualifiedAt.Equal(deposit.At) || state.BetTotal != "20000" {
		t.Fatal(state, err)
	}
	noInvite := member
	noInvite.ParentID = nil
	state, err = Evaluate(p, noInvite, []Fact{deposit, bet})
	if err != nil || state.Effective {
		t.Fatal("未受邀会员不能计为邀请有效会员", state, err)
	}
	invalid := bet
	other := "CASINO"
	invalid.Venue = &other
	if _, err = Evaluate(p, member, []Fact{invalid}); err == nil {
		t.Fatal("场馆未过滤")
	}
	invalid = deposit
	invalid.Currency = "BTC"
	if _, err = Evaluate(p, member, []Fact{invalid}); err == nil {
		t.Fatal("混币累计")
	}
	invalid = deposit
	invalid.Type = "DEPOSIT_FAILED"
	if _, err = Evaluate(p, member, []Fact{invalid}); err == nil {
		t.Fatal("失败充值被累计")
	}
	invalid = deposit
	invalid.At = member.RegisteredAt.Add(-time.Second)
	if _, err = Evaluate(p, member, []Fact{invalid}); err == nil {
		t.Fatal("注册前事实被累计")
	}
}
func TestAmountsAndImmutableCriteria(t *testing.T) {
	for _, amount := range []string{"-1", "1.5", "01", "1e5", "", "999999999999999999999999999999999999999"} {
		if _, err := Amount(amount); err == nil {
			t.Fatalf("非法最小单位金额 %q", amount)
		}
	}
	p := Policy{Deposit: "0", Bet: "100", Venues: []string{"SPORTS"}}
	if ValidatePolicy(p) == nil {
		t.Fatal("允许零门槛")
	}
	p.Deposit = "1"
	p.Venues = []string{"SPORTS", "SPORTS"}
	if ValidatePolicy(p) == nil {
		t.Fatal("重复场馆")
	}
}
