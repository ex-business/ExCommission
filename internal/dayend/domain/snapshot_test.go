package domain

import (
	"testing"
	"time"
)

func TestCustomTimezoneDayBoundaries(t *testing.T) {
	for _, c := range []struct {
		day, zone string
		hours     int
	}{{"2026-03-08", "America/New_York", 23}, {"2026-11-01", "America/New_York", 25}, {"2026-10-16", "Asia/Shanghai", 24}} {
		start, end, err := Bounds(c.day, c.zone)
		if err != nil || end.Sub(start) != time.Duration(c.hours)*time.Hour {
			t.Fatalf("%+v %v %v %v", c, start, end, err)
		}
	}
	for _, zone := range []string{"", "Local", "Mars/Test"} {
		if _, _, err := Bounds("2026-10-16", zone); err == nil {
			t.Fatal(zone)
		}
	}
}
