package domain

import (
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	"testing"
	"time"
)

func profile(at time.Time) affiliate.Version {
	return affiliate.Version{Revision: 1, Effective: at, Config: affiliate.Config{Enabled: true, Currency: "USD", CurrencyScale: 2, Timezone: "UTC", GradePeriod: "DAY", GradeCondition: affiliate.Threshold{Deposit: "100", Turnover: "100"}, ReferralCondition: affiliate.Threshold{Deposit: "100", Turnover: "200"}, InviteeCondition: affiliate.Threshold{Deposit: "100", Turnover: "300"}, InviteeReward: "50", Levels: []affiliate.Level{{Number: 0, Turnover: "0", Rates: [3]int{10000, 5000, 1000}}}, Steps: []affiliate.Step{{Number: 1, Members: 1, Amount: "100"}, {Number: 2, Members: 3, Amount: "200"}}, VenueIDs: []string{"SPORTS"}}}
}
func TestThreeConditionsLifetimeInviteeAndIncrementalReferral(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	now := at.Add(time.Hour)
	v := profile(at)
	root := Member{UserID: "root", AgentID: "a-root", RegisteredAt: at}
	child := Member{UserID: "child", AgentID: "a-child", ParentUser: "root", RegisteredAt: at}
	gate := Gate{Enabled: true, Since: at}
	facts := []affiliate.Fact{{ID: "dep", UserID: "child", Kind: "DEPOSIT_SUCCEEDED", Currency: "USD", Amount: "100", ValidBet: "0", At: at.Add(time.Minute)}, {ID: "bet", UserID: "child", Kind: "BET_SETTLED", Currency: "USD", Amount: "200", ValidBet: "200", VenueID: "SPORTS", At: at.Add(2 * time.Minute)}}
	p, created, e := Evaluate(v, child, []Member{root, child}, facts, gate, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	if p.Qualification.Grade.At == nil || p.Qualification.Referral.At == nil || p.Qualification.Invitee.At != nil || len(created) != 0 {
		t.Fatal("三套条件串用", p, created)
	}
	p, created, e = Evaluate(v, root, []Member{root, child}, facts, gate, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(created) != 1 || created[0].Kind != Referral || created[0].UserID != "root" || p.Steps[1].CumulativeRequired != 4 || p.Steps[1].Completed != 0 {
		t.Fatal(p, created)
	}
	old := created
	// 新送达一个更早达标的会员，不能重新占用已解锁的第一阶梯。
	other := Member{UserID: "earlier", AgentID: "a-earlier", ParentUser: "root", RegisteredAt: at}
	regs := []Member{root, child, other}
	facts = append(facts, affiliate.Fact{ID: "dep2", UserID: "earlier", Kind: "DEPOSIT_SUCCEEDED", Currency: "USD", Amount: "100", ValidBet: "0", At: at.Add(time.Second)}, affiliate.Fact{ID: "bet2", UserID: "earlier", Kind: "BET_SETTLED", Currency: "USD", Amount: "300", ValidBet: "300", VenueID: "SPORTS", At: at.Add(2 * time.Second)})
	p, created, e = Evaluate(v, root, regs, facts, gate, old, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(created) != 0 || p.Steps[0].Members[0] != "child" || p.Steps[1].Members[0] != "earlier" || p.Steps[0].QualifiedAt.Year() != 2026 {
		t.Fatal("旧阶梯或真实达标时间被改写", p, created)
	}
	facts = append(facts, affiliate.Fact{ID: "bet3", UserID: "child", Kind: "BET_SETTLED", Currency: "USD", Amount: "100", ValidBet: "100", VenueID: "SPORTS", At: at.Add(3 * time.Minute)})
	p, created, e = Evaluate(v, child, regs, facts, gate, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(created) != 1 || created[0].Kind != Invitee || created[0].UserID != "child" {
		t.Fatal("受邀奖励受益人错误", created)
	}
	_, again, e := Evaluate(v, child, regs, facts, gate, created, now)
	if e != nil || len(again) != 0 {
		t.Fatal("受邀奖励重复解锁", again, e)
	}
	_, none, e := Evaluate(v, root, regs, facts, Gate{Enabled: false, Since: at}, nil, now)
	if e != nil || len(none) != 0 {
		t.Fatal("关闭代理仍新增奖励", none, e)
	}
	p, none, e = Evaluate(v, child, regs, facts, Gate{Enabled: true, Since: now}, nil, now)
	if e != nil || len(none) != 0 || p.Qualification.Invitee.Deposit != "0" {
		t.Fatal("恢复追补了关闭前事实", p, none, e)
	}
}
