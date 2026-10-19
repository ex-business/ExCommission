// Package domain 定义代理业务闭环的事实、进度及奖励状态；金额计算复用 affiliate 领域规则。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"strconv"
	"time"
)

const (
	Referral   = "REFERRAL"
	Invitee    = "INVITEE"
	Commission = "COMMISSION"
)

type Event struct {
	Identity     *affiliate.RiskIdentity `json:"risk_identity,omitempty"`
	BusinessID   string                  `json:"business_id"`
	Source       string                  `json:"source_id"`
	EventID      string                  `json:"event_id"`
	ExternalUser string                  `json:"external_user_id"`
	Kind         string                  `json:"kind"`
	Currency     string                  `json:"currency"`
	Amount       string                  `json:"amount_minor"`
	ValidBet     string                  `json:"valid_bet_minor"`
	Venue        string                  `json:"venue_type_id"`
	At           time.Time               `json:"business_time"`
}

func (e Event) Fact(user string) affiliate.Fact {
	return affiliate.Fact{Identity: e.Identity, ID: ID(e.Source, e.Kind, e.BusinessID), UserID: user, Kind: e.Kind, Currency: e.Currency, Amount: e.Amount, ValidBet: e.ValidBet, VenueID: e.Venue, At: e.At}
}
func (e Event) Validate(c affiliate.Config, now time.Time) error {
	for _, s := range []string{e.Source, e.EventID, e.BusinessID, e.ExternalUser} {
		if !foundation.ValidID(s) {
			return foundation.ErrValidation
		}
	}
	if e.At.Year() < 1000 {
		return foundation.ErrValidation
	}
	return e.Fact(e.ExternalUser).Validate(c, now)
}
func ID(parts ...string) string {
	raw, _ := json.Marshal(parts)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

type Member struct {
	UserID       string    `json:"user_id"`
	AgentID      string    `json:"agent_id"`
	ParentUser   string    `json:"parent_user_id"`
	RegisteredAt time.Time `json:"registered_at"`
}
type Gate struct {
	Enabled  bool      `json:"enabled"`
	Since    time.Time `json:"enabled_since"`
	Revision uint64    `json:"revision"`
	Reason   string    `json:"reason"`
	Actor    string    `json:"actor_id"`
	Updated  time.Time `json:"updated_at"`
}
type Qualified struct {
	UserID   string                  `json:"user_id"`
	Grade    affiliate.Qualification `json:"grade"`
	Referral affiliate.Qualification `json:"referral"`
	Invitee  affiliate.Qualification `json:"invitee"`
}
type Reward struct {
	WalletDelivery    string             `json:"wallet_delivery_id,omitempty"`
	WalletDestination string             `json:"wallet_destination,omitempty"`
	WalletReceipt     *string            `json:"wallet_receipt_ref,omitempty"`
	ProgramID         string             `json:"program_id,omitempty"`
	ProgramConfig     *affiliate.Version `json:"program_configuration,omitempty"`
	Profile           *MemberProfile     `json:"profile,omitempty"`
	Since             time.Time          `json:"accumulation_since"`
	Qualifications    []Qualified        `json:"member_qualifications"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	ID                string             `json:"reward_id"`
	UserID            string             `json:"user_id"`
	AgentID           string             `json:"agent_id"`
	Kind              string             `json:"kind"`
	Step              int                `json:"step"`
	Day               string             `json:"business_day"`
	Currency          string             `json:"currency"`
	Amount            string             `json:"amount_minor"`
	Original          string             `json:"calculated_minor"`
	ConfigRevision    uint64             `json:"config_revision"`
	Status            string             `json:"status"`
	UnlockedAt        time.Time          `json:"unlocked_at"`
	Members           []string           `json:"qualified_user_ids"`
	EntitlementID     string             `json:"entitlement_id"`
	Reviewer          string             `json:"reviewer_id"`
	ReviewedAt        *time.Time         `json:"reviewed_at"`
	Reason            string             `json:"reason"`
	ClaimedAt         *time.Time         `json:"claimed_at"`
	ClaimID           string             `json:"claim_id"`
	ClaimIP           string             `json:"claim_ip,omitempty"`
	Revision          uint64             `json:"revision"`
}
type Progress struct {
	BlockedReason  string                   `json:"blocked_reason"`
	ConfigRevision uint64                   `json:"config_revision"`
	Enabled        bool                     `json:"enabled"`
	Gate           Gate                     `json:"gate"`
	IsInvited      bool                     `json:"is_invited"`
	Qualification  Qualified                `json:"qualification"`
	Steps          []affiliate.StepProgress `json:"steps"`
	Rewards        []Reward                 `json:"rewards"`
}

// Evaluate 仅描述本次候选解锁，持久化层用奖励业务ID确保终身一次；已解锁会员占用不因暂停或规则变化释放。
func Evaluate(v affiliate.Version, m Member, regs []Member, facts []affiliate.Fact, g Gate, prior []Reward, now time.Time) (Progress, []Reward, error) {
	out := Progress{ConfigRevision: v.Revision, Enabled: v.Config.Enabled && g.Enabled, Gate: g, IsInvited: m.ParentUser != "", Steps: []affiliate.StepProgress{}, Rewards: prior, Qualification: Qualified{UserID: m.UserID}}
	c := v.Config
	if e := c.Normalize(); e != nil {
		return out, nil, e
	}
	cutoff := now.Add(time.Microsecond)
	grouped := map[string][]affiliate.Fact{}
	for _, f := range facts {
		// 关闭区间不追补；重开后使用新有效起点，已持久化的解锁权益仍保留。
		if !f.At.Before(g.Since) {
			grouped[f.UserID] = append(grouped[f.UserID], f)
		}
	}
	var e error
	out.Qualification, e = SnapshotQualification(c, m.UserID, grouped[m.UserID], cutoff)
	if e != nil {
		return out, nil, e
	}
	filtered := []Reward{}
	for _, r := range prior {
		if r.ProgramID == v.ProgramID {
			filtered = append(filtered, r)
		}
	}
	prior = filtered
	out.Rewards = filtered
	existing := map[string]Reward{}
	qualified := map[string]time.Time{}
	for _, r := range prior {
		existing[r.ID] = r
		if r.Kind == Referral {
			for _, id := range r.Members {
				qualified[id] = r.UnlockedAt
			}
		}
	}
	for _, child := range regs {
		if child.ParentUser != m.UserID {
			continue
		}
		if _, ok := qualified[child.UserID]; ok {
			continue
		}
		q, err := affiliate.Qualify(c.ReferralCondition, grouped[child.UserID], c, cutoff)
		if err != nil {
			return out, nil, err
		}
		if q.At != nil {
			qualified[child.UserID] = *q.At
		}
	}
	// 已解锁的前缀直接采用持久化会员及真实时间；新会员只参与剩余阶梯，不重新排序历史占用。
	assigned := map[string]bool{}
	cumulative := 0
	for _, step := range c.Steps {
		old, ok := existing[versionRewardID(v, Referral, m.UserID, step.Number)]
		if !ok {
			break
		}
		// 已解锁权益沿用原人数及金额；新版本仅影响每位会员尚未解锁的阶梯，禁止追改或重复发放。
		step.Members = len(old.Members)
		step.Amount = old.Original
		cumulative += step.Members
		at := old.UnlockedAt
		out.Steps = append(out.Steps, affiliate.StepProgress{Step: step, Completed: len(old.Members), CumulativeRequired: cumulative, Unlocked: true, QualifiedAt: &at, Members: append([]string(nil), old.Members...)})
		for _, id := range old.Members {
			assigned[id] = true
		}
	}
	ordered := []affiliate.QualifiedUser{}
	for id, at := range qualified {
		if !assigned[id] {
			ordered = append(ordered, affiliate.QualifiedUser{UserID: id, At: at})
		}
	}
	remaining := c
	remaining.Steps = c.Steps[len(out.Steps):]
	next, err := remaining.StepProgress(ordered)
	if err != nil {
		return out, nil, err
	}
	for n := range next {
		next[n].CumulativeRequired += cumulative
	}
	out.Steps = append(out.Steps, next...)
	candidates := []Reward{}
	add := func(kind string, step int, amount string, at time.Time, members []string) error {
		id := versionRewardID(v, kind, m.UserID, step)
		if _, ok := existing[id]; ok {
			return nil
		}
		if at.Before(v.Effective) {
			at = v.Effective
		}
		if at.Before(g.Since) {
			at = g.Since
		}
		details := []Qualified{}
		for _, user := range members {
			q, e := SnapshotQualification(c, user, grouped[user], cutoff)
			if e != nil {
				return e
			}
			details = append(details, q)
		}
		candidates = append(candidates, Reward{Since: g.Since, Qualifications: details, ID: id, UserID: m.UserID, AgentID: m.AgentID, Kind: kind, Step: step, Currency: c.Currency, Amount: amount, Original: amount, ConfigRevision: v.Revision, CreatedAt: now, UpdatedAt: now, Status: "CLAIMABLE", UnlockedAt: at, Members: members, Revision: 1})
		return nil
	}
	if out.Enabled {
		if out.IsInvited && out.Qualification.Invitee.At != nil {
			if e := add(Invitee, 0, c.InviteeReward, *out.Qualification.Invitee.At, []string{m.UserID}); e != nil {
				return out, nil, e
			}
		}
		for _, s := range out.Steps {
			if s.Unlocked && s.QualifiedAt != nil {
				if e := add(Referral, s.Number, s.Amount, *s.QualifiedAt, s.Members); e != nil {
					return out, nil, e
				}
			}
		}
	}
	return out, candidates, nil
}

func stringStep(v int) string { return strconv.Itoa(v) }

// BusinessHash 的字段顺序是来源清单协议的一部分，与JSON请求字段顺序无关，不包含可变化的投递事件号。
func (e Event) BusinessHash() string {
	parts := []string{e.Source, e.Kind, e.BusinessID, e.ExternalUser, e.Currency, e.Amount, e.ValidBet, e.Venue, e.At.UTC().Format(time.RFC3339Nano)}
	// 旧事实清单摘要保持原协议；新识别字段必须参与摘要，禁止同订单替换风控身份。
	if e.Identity != nil {
		parts = append(parts, "risk-v1", e.Identity.IP, e.Identity.Device, e.Identity.Account)
	}
	return ID(parts...)
}

type Watermark struct {
	Source string `json:"source_id"`
	Day    string `json:"business_day"`
	Count  uint64 `json:"fact_count"`
	Digest string `json:"facts_digest"`
}
type FrozenCommission struct {
	Since    time.Time                 `json:"accumulation_since"`
	AgentID  string                    `json:"agent_id"`
	Eligible bool                      `json:"eligible"`
	Result   affiliate.DailyCommission `json:"calculation"`
}
type Daily struct {
	// 冻结结果永不覆盖，冲正后的结算依据独立保存，便于核对原始计算与调整差额。
	RestatedResults []FrozenCommission `json:"restated_results,omitempty"`
	Day             string             `json:"business_day"`
	Status          string             `json:"status"`
	Config          affiliate.Version  `json:"configuration"`
	Watermarks      []Watermark        `json:"watermarks"`
	Results         []FrozenCommission `json:"results"`
	FrozenAt        time.Time          `json:"frozen_at"`
	SettledAt       *time.Time         `json:"settled_at"`
}

// SnapshotQualification 保存解锁时的三套事实累计与首次达标时间，后续事实不会重写奖励弹窗的审核依据。
func SnapshotQualification(c affiliate.Config, user string, facts []affiliate.Fact, cutoff time.Time) (Qualified, error) {
	q := Qualified{UserID: user}
	var e error
	q.Grade, e = affiliate.Qualify(c.GradeCondition, facts, c, cutoff)
	if e != nil {
		return q, e
	}
	q.Referral, e = affiliate.Qualify(c.ReferralCondition, facts, c, cutoff)
	if e != nil {
		return q, e
	}
	q.Invitee, e = affiliate.Qualify(c.InviteeCondition, facts, c, cutoff)
	return q, e
}

// 独立方案共享真实会员与账簿，但奖励业务键包含方案，避免叠加方案互相覆盖；旧键保持不变。
func versionRewardID(v affiliate.Version, kind, user string, step int) string {
	if v.ProgramID == "" {
		return ID(kind, user, stringStep(step))
	}
	return ID("program", v.ProgramID, kind, user, "", stringStep(step))
}
