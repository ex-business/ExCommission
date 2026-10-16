package domain

import (
	"bytes"
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"sort"
	"time"
)

type Config struct {
	Configured bool       `json:"configured"`
	Enabled    bool       `json:"enabled"`
	PlanID     string     `json:"plan_id"`
	Venues     []string   `json:"venue_ids"`
	Revision   uint64     `json:"revision"`
	Effective  *time.Time `json:"effective_from"`
}
type Change struct {
	Enabled  bool     `json:"enabled"`
	PlanID   string   `json:"plan_id"`
	Venues   []string `json:"venue_ids"`
	Revision uint64   `json:"expected_revision"`
	Reason   string   `json:"reason"`
}

func (c *Change) Validate() error {
	if c.Venues == nil || !foundation.ValidID(c.PlanID) || !foundation.ValidID(c.Reason) || len(c.Venues) > 100 || (c.Enabled && len(c.Venues) == 0) {
		return foundation.ErrValidation
	}
	seen := map[string]bool{}
	for _, v := range c.Venues {
		if !foundation.ValidID(v) || seen[v] {
			return foundation.ErrValidation
		}
		seen[v] = true
	}
	sort.Strings(c.Venues)
	return nil
}
func (c Config) AllowsVenue(venue string) bool {
	if !c.Configured {
		return true
	}
	for _, v := range c.Venues {
		if v == venue {
			return true
		}
	}
	return false
}

func (c *Change) UnmarshalJSON(body []byte) error {
	var w struct {
		Enabled  *bool    `json:"enabled"`
		PlanID   string   `json:"plan_id"`
		Venues   []string `json:"venue_ids"`
		Revision *uint64  `json:"expected_revision"`
		Reason   string   `json:"reason"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return foundation.ErrValidation
	}
	if d.Decode(&struct{}{}) != io.EOF || w.Enabled == nil || w.Revision == nil || w.Venues == nil {
		return foundation.ErrValidation
	}
	*c = Change{Enabled: *w.Enabled, PlanID: w.PlanID, Venues: w.Venues, Revision: *w.Revision, Reason: w.Reason}
	return nil
}
