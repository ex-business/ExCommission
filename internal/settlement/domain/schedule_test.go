package domain

import (
	"testing"
	"time"
)

func TestDailyOneUTCAndDateBoundaries(t *testing.T) {
	due, err := DueAt("2026-12-31")
	if err != nil || due.Format(time.RFC3339) != "2027-01-01T01:00:00Z" {
		t.Fatal(due, err)
	}
	if !due.Add(-time.Second).Before(due) || due.Before(due) {
		t.Fatal("due boundary")
	}
	if next, e := NextPeriod("2026-12-31"); e != nil || next != "2027-01-01" {
		t.Fatal(next, e)
	}
	sydney := time.FixedZone("UTC+11", 11*3600)
	clock := time.Date(2027, 1, 1, 10, 30, 0, 0, sydney)
	if got := InitialPeriod(clock); got != "2026-12-30" {
		t.Fatal("used local calendar", got)
	}
	if _, e := DueAt("2026-02-30"); e == nil {
		t.Fatal("invalid date")
	}
}
