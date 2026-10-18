// Package affiliateprograms 管理独立代理方案；发布、运行区间和命中记录均保留历史，不改写旧权益。
package affiliateprograms

import (
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"sort"
	"time"
)

type Match struct {
	Sources  []string `json:"sources"`
	Kinds    []string `json:"event_types"`
	Users    []string `json:"member_ids"`
	Mode     string   `json:"mode"`
	Group    string   `json:"group"`
	Priority int      `json:"priority"`
}
type Change struct {
	ID        string           `json:"program_id"`
	Name      string           `json:"name"`
	Expected  uint64           `json:"expected_revision"`
	State     string           `json:"state"`
	Effective time.Time        `json:"effective_from"`
	Reason    string           `json:"reason"`
	Match     Match            `json:"match"`
	Config    affiliate.Config `json:"config"`
}
type Version struct {
	Change
	Revision    uint64    `json:"revision"`
	ApprovalID  string    `json:"approval_id"`
	PublishedAt time.Time `json:"published_at"`
}
type Decision struct {
	ProgramID string `json:"program_id"`
	Revision  uint64 `json:"revision"`
	Selected  bool   `json:"selected"`
	Reason    string `json:"reason"`
}

func (c *Change) Validate() error {
	if !foundation.ValidID(c.ID) || len(c.ID) > 64 || c.ID == "legacy" || len(c.Name) == 0 || len(c.Name) > 120 || !foundation.ValidID(c.Reason) || c.Effective.IsZero() || !c.Effective.Equal(c.Effective.Truncate(time.Microsecond)) {
		return foundation.ErrValidation
	}
	if c.State != "READY" && c.State != "RUNNING" && c.State != "STOPPED" {
		return foundation.ErrValidation
	}
	if c.Config.Normalize() != nil || !c.Config.Enabled {
		return foundation.ErrValidation
	}
	if c.Match.Mode != "STACK" && c.Match.Mode != "PRIORITY" && c.Match.Mode != "EXCLUSIVE" {
		return foundation.ErrValidation
	}
	if c.Match.Mode != "STACK" && (!foundation.ValidID(c.Match.Group) || c.Match.Group == "legacy") {
		return foundation.ErrValidation
	}
	if c.Match.Mode == "STACK" && c.Match.Group != "" {
		return foundation.ErrValidation
	}
	for _, list := range [][]string{c.Match.Sources, c.Match.Kinds, c.Match.Users} {
		if len(list) > 1000 {
			return foundation.ErrValidation
		}
		seen := map[string]bool{}
		for _, v := range list {
			if !foundation.ValidID(v) || seen[v] {
				return foundation.ErrValidation
			}
			seen[v] = true
		}
	}
	for _, k := range c.Match.Kinds {
		if k != "DEPOSIT_SUCCEEDED" && k != "BET_SETTLED" {
			return foundation.ErrValidation
		}
	}
	return nil
}
func contains(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func (v Version) Matches(e flow.Event, user string) bool {
	return v.State == "RUNNING" && !e.At.Before(v.Effective) && e.Currency == v.Config.Currency && contains(v.Match.Sources, e.Source) && contains(v.Match.Kinds, e.Kind) && contains(v.Match.Users, user) && (e.Kind != "BET_SETTLED" || v.Config.AllowsVenue(e.Venue))
}

// Select 按业务发生时间选出的不可变版本匹配；同组互斥或同优先级歧义必须阻断，不能随机选择。
func Select(versions []Version, e flow.Event, user string) ([]Version, []Decision, error) {
	selected := []Version{}
	decisions := []Decision{}
	groups := map[string][]Version{}
	for _, v := range versions {
		if !v.Matches(e, user) {
			continue
		}
		if v.Match.Mode == "STACK" {
			selected = append(selected, v)
		} else {
			groups[v.Match.Group] = append(groups[v.Match.Group], v)
		}
	}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].Match.Priority > group[j].Match.Priority })
		if len(group) > 1 && (group[0].Match.Mode == "EXCLUSIVE" || group[1].Match.Mode == "EXCLUSIVE" || group[0].Match.Priority == group[1].Match.Priority) {
			return nil, nil, errors.New("STATE_CONFLICT:AMBIGUOUS_PROGRAM_MATCH")
		}
		selected = append(selected, group[0])
		for _, v := range group[1:] {
			decisions = append(decisions, Decision{v.ID, v.Revision, false, "LOWER_PRIORITY"})
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	for _, v := range selected {
		decisions = append(decisions, Decision{v.ID, v.Revision, true, "MATCHED"})
	}
	return selected, decisions, nil
}
func ConfigVersion(v Version) affiliate.Version {
	return affiliate.Version{ProgramID: v.ID, Revision: v.Revision, Config: v.Config, Effective: v.Effective, ApprovalID: v.ApprovalID}
}
func RewardID(program, kind, user string, step int, day string) string {
	return flow.ID("program", program, kind, user, day, stepString(step))
}
func stepString(v int) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	s := ""
	for v > 0 {
		s = string(digits[v%10]) + s
		v /= 10
	}
	return s
}

// CanProcess 运行中修订仍结清同一连续运行区间的旧版本；一旦停止，重启只处理新运行区间，禁止隐式补结算。
func CanProcess(all []Version, version Version, now time.Time) bool {
	if version.State != "RUNNING" || version.Effective.After(now) {
		return false
	}
	currentRunning := false
	for _, v := range Active(all, now) {
		if v.ID == version.ID && v.State == "RUNNING" {
			currentRunning = true
		}
	}
	if !currentRunning {
		return false
	}
	for _, v := range all {
		if v.ID == version.ID && v.Revision > version.Revision && !v.Effective.After(now) && v.State != "RUNNING" {
			return false
		}
	}
	return true
}
