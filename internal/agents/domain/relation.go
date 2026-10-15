package domain

import (
	"errors"
	"time"
)

var ErrCycle = errors.New("RELATION_CYCLE")
var ErrHistory = errors.New("RELATION_HISTORY_MISSING")
var ErrNotFound = errors.New("NOT_FOUND")
var ErrStale = errors.New("STALE_APPROVAL")

type RelationChange struct {
	AgentID          string  `json:"agent_id"`
	NewParentID      *string `json:"new_parent_id"`
	ExpectedRevision uint64  `json:"expected_revision"`
	Reason           string  `json:"reason"`
}
type Ancestor struct {
	AgentID       string     `json:"agent_id"`
	Depth         int        `json:"depth"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
}

type Account struct {
	ID, UserID, Status string
	Created            time.Time
}
