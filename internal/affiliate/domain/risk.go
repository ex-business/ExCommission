package domain

import (
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RiskIdentity 只能由已验签的来源提供不可逆识别摘要，禁止存储原始设备标识或共享账户信息。
type RiskIdentity struct {
	IP      string `json:"ip_hash,omitempty"`
	Device  string `json:"device_hash,omitempty"`
	Account string `json:"account_hash,omitempty"`
}

func (r *RiskIdentity) Validate() error {
	if r == nil {
		return nil
	}
	for _, v := range []string{r.IP, r.Device, r.Account} {
		if v != "" {
			b, e := hex.DecodeString(v)
			if e != nil || len(b) != 32 {
				return ErrValidation
			}
		}
	}
	return nil
}

// RiskRule 每个维度独立配置周期与范围；按配置顺序去重，只影响计佣业务量，不撤销历史权益。
type RiskRule struct {
	ID        string     `json:"rule_id"`
	Enabled   bool       `json:"enabled"`
	Dimension string     `json:"dimension"`
	Period    string     `json:"period"`
	Days      int        `json:"window_days"`
	Scope     string     `json:"scope"`
	Missing   string     `json:"missing_identity"`
	From      time.Time  `json:"effective_from"`
	Until     *time.Time `json:"effective_until,omitempty"`
}

func (r RiskRule) Validate() error {
	if r.ID == "" || len(r.ID) > 64 || r.From.IsZero() || !r.From.Equal(r.From.Truncate(time.Microsecond)) {
		return ErrValidation
	}
	switch r.Dimension {
	case "IP", "DEVICE", "ACCOUNT":
	default:
		return ErrValidation
	}
	switch r.Period {
	case "DAY", "WEEK", "MONTH", "LIFETIME":
		if r.Days != 0 {
			return ErrValidation
		}
	case "CUSTOM_DAYS":
		if r.Days < 1 || r.Days > 36600 {
			return ErrValidation
		}
	default:
		return ErrValidation
	}
	if r.Scope != "SITE" && r.Scope != "BENEFICIARY" {
		return ErrValidation
	}
	if r.Missing != "ALLOW" && r.Missing != "DENY" {
		return ErrValidation
	}
	if r.Until != nil && (!r.Until.After(r.From) || !r.Until.Equal(r.Until.Truncate(time.Microsecond))) {
		return ErrValidation
	}
	return nil
}

type RiskDecision struct {
	FactID string `json:"fact_id"`
	UserID string `json:"user_id"`
	RuleID string `json:"rule_id"`
	Reason string `json:"reason"`
	Winner string `json:"winner_user_id,omitempty"`
}

func (r RiskRule) bucket(at time.Time, loc *time.Location) string {
	v := at.In(loc)
	switch r.Period {
	case "DAY":
		return v.Format("2006-01-02")
	case "WEEK":
		y, w := v.ISOWeek()
		return strconv.Itoa(y) + "-" + strconv.Itoa(w)
	case "MONTH":
		return v.Format("2006-01")
	case "CUSTOM_DAYS":
		// 按站点自然日计数，夏令时23/25小时日不会改变窗口所属日期。
		midnight := func(t time.Time) time.Time {
			v := t.In(loc)
			return time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC)
		}
		return strconv.FormatInt(int64(midnight(at).Sub(midnight(r.From)).Hours()/24)/int64(r.Days), 10)
	default:
		return "LIFETIME"
	}
}

// CommissionRisk 同组首笔有效投注的会员获得该周期资格，其后同会员的订单仍全部计入。
// 各规则独立执行；先按业务时间和事实ID排序，重放、来源乱序与并发不会改变赢家。
func CommissionRisk(c Config, facts []Fact, relevant map[string]bool) (map[string]bool, []RiskDecision) {
	excluded := map[string]bool{}
	decisions := []RiskDecision{}
	ordered := append([]Fact(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].At.Before(ordered[j].At)
	})
	for _, r := range c.RiskRules {
		if !r.Enabled {
			continue
		}
		winners := map[string]string{}
		for _, f := range ordered {
			if excluded[f.ID] || f.Kind != "BET_SETTLED" || f.ValidBet == "0" || !c.AllowsVenue(f.VenueID) || f.At.Before(r.From) || (r.Until != nil && !f.At.Before(*r.Until)) || (r.Scope == "BENEFICIARY" && !relevant[f.ID]) {
				continue
			}
			value := ""
			if f.Identity != nil {
				switch r.Dimension {
				case "IP":
					value = f.Identity.IP
				case "DEVICE":
					value = f.Identity.Device
				case "ACCOUNT":
					value = f.Identity.Account
				}
			}
			reason, winner := "", ""
			if value == "" {
				if r.Missing == "DENY" {
					reason = "IDENTITY_MISSING"
				}
			} else {
				key := r.bucket(f.At, c.Location()) + ":" + strings.ToLower(value)
				winner = winners[key]
				if winner == "" {
					winners[key] = f.UserID
				} else if winner != f.UserID {
					reason = "DUPLICATE_" + r.Dimension
				}
			}
			if reason != "" {
				excluded[f.ID] = true
				if relevant[f.ID] {
					decisions = append(decisions, RiskDecision{f.ID, f.UserID, r.ID, reason, winner})
				}
			}
		}
	}
	return excluded, decisions
}
