package affiliateprograms

import (
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	"testing"
	"time"
)

func TestSelectionModesAndBusinessTime(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	ev := flow.Event{Source: "source", Kind: "DEPOSIT_SUCCEEDED", Currency: "USD", At: now}
	makeV := func(id, mode string, priority int) Version {
		return Version{Change: Change{ID: id, State: "RUNNING", Effective: now.Add(-time.Hour), Match: Match{Mode: mode, Group: "g", Priority: priority}}, Revision: 1}
	}
	a, b := makeV("a", "PRIORITY", 1), makeV("b", "PRIORITY", 2)
	a.Config.Currency = "USD"
	b.Config.Currency = "USD"
	picked, decisions, e := Select([]Version{a, b}, ev, "user")
	if e != nil || len(picked) != 1 || picked[0].ID != "b" || len(decisions) != 2 {
		t.Fatal(picked, decisions, e)
	}
	b.Match.Priority = 1
	if _, _, e = Select([]Version{a, b}, ev, "user"); e == nil {
		t.Fatal("并列优先级必须拒绝")
	}
	a.Match.Mode = "EXCLUSIVE"
	if _, _, e = Select([]Version{a, b}, ev, "user"); e == nil {
		t.Fatal("互斥重叠必须拒绝")
	}
	a.Match.Mode = "STACK"
	b.Match.Mode = "STACK"
	picked, _, e = Select([]Version{a, b}, ev, "user")
	if e != nil || len(picked) != 2 {
		t.Fatal(picked, e)
	}
	a.State = "STOPPED"
	b.Effective = now.Add(time.Second)
	picked, _, e = Select([]Version{a, b}, ev, "user")
	if e != nil || len(picked) != 0 {
		t.Fatal("未启动或未生效不得命中", picked, e)
	}
	old := b
	old.Revision = 1
	old.State = "RUNNING"
	old.Effective = now.Add(-time.Hour)
	next := old
	next.Revision = 2
	next.State = "STOPPED"
	next.Effective = now
	if Active([]Version{old, next}, now.Add(-time.Microsecond))[0].Revision != 1 || Active([]Version{old, next}, now)[0].State != "STOPPED" {
		t.Fatal("生效边界不正确")
	}
}
func TestContinuousRunAndRestart(t *testing.T) {
	now := time.Now().UTC()
	a := Version{Change: Change{ID: "a", State: "RUNNING", Effective: now.Add(-3 * time.Hour)}, Revision: 1}
	b := a
	b.Revision = 2
	b.Effective = now.Add(-2 * time.Hour)
	if !CanProcess([]Version{a, b}, a, now) {
		t.Fatal("连续运行修订应结清旧版本")
	}
	stop := b
	stop.Revision = 3
	stop.State = "STOPPED"
	stop.Effective = now.Add(-time.Hour)
	if CanProcess([]Version{a, b, stop}, a, now) {
		t.Fatal("停止后仍新增旧版本结算")
	}
	restart := stop
	restart.Revision = 4
	restart.State = "RUNNING"
	restart.Effective = now
	if CanProcess([]Version{a, b, stop, restart}, a, now) || !CanProcess([]Version{a, b, stop, restart}, restart, now) {
		t.Fatal("恢复不应自动补旧区间")
	}
}
