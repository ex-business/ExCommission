package domain

import (
	"encoding/json"
	"testing"
)

func TestSiteConfigExplicitSwitchAndUniqueVenues(t *testing.T) {
	for _, raw := range []string{`{"plan_id":"P1","venue_ids":[],"expected_revision":0,"reason":"reason"}`, `{"enabled":false,"plan_id":"P1","venue_ids":null,"expected_revision":0,"reason":"reason"}`, `{"enabled":false,"plan_id":"P1","venue_ids":[],"reason":"reason"}`, `{"enabled":false,"plan_id":"P1","venue_ids":[],"expected_revision":0,"reason":"reason","admin":true}`} {
		var c Change
		if json.Unmarshal([]byte(raw), &c) == nil {
			t.Fatal("invalid contract", raw)
		}
	}
	c := Change{Enabled: true, PlanID: "P1", Venues: []string{"A", "A"}, Reason: "reason"}
	if c.Validate() == nil {
		t.Fatal("duplicate venue")
	}
	c.Venues = []string{"B", "A"}
	if e := c.Validate(); e != nil || c.Venues[0] != "A" {
		t.Fatal(c, e)
	}
}
