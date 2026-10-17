package domain

import (
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"sort"
	"time"
)

type Registration struct {
	UserID   string    `json:"user_id"`
	ParentID string    `json:"parent_user_id"`
	At       time.Time `json:"registered_at"`
}
type Fact struct {
	Identity *RiskIdentity `json:"risk_identity,omitempty"`
	ID       string        `json:"event_id"`
	UserID   string        `json:"user_id"`
	Kind     string        `json:"kind"`
	Currency string        `json:"currency"`
	Amount   string        `json:"amount_minor"`
	ValidBet string        `json:"valid_bet_minor"`
	VenueID  string        `json:"venue_type_id"`
	At       time.Time     `json:"business_time"`
}

func (f Fact) Validate(c Config, now time.Time) error {
	if e := f.Identity.Validate(); e != nil {
		return e
	}

	if !foundation.ValidID(f.ID) || !foundation.ValidID(f.UserID) || f.Currency != c.Currency || f.At.IsZero() || f.At.After(now) || !f.At.Equal(f.At.Truncate(time.Microsecond)) {
		return ErrValidation
	}
	n, e := Money(f.Amount, true)
	if e != nil {
		return e
	}
	switch f.Kind {
	case "DEPOSIT_SUCCEEDED", "WITHDRAWAL_SUCCEEDED":
		if f.ValidBet != "0" || f.VenueID != "" {
			return ErrValidation
		}
	case "BET_SETTLED":
		v, e := Money(f.ValidBet, false)
		if e != nil || v.Cmp(n) > 0 || !foundation.ValidID(f.VenueID) {
			return ErrValidation
		}
	default:
		return ErrValidation
	}
	return nil
}

type Qualification struct {
	Deposit  string     `json:"deposit_minor"`
	Turnover string     `json:"valid_bet_minor"`
	At       *time.Time `json:"qualified_at"`
}

// Qualify records the first instant both lifetime requirements were satisfied, regardless of delivery order.
func Qualify(t Threshold, facts []Fact, c Config, cutoff time.Time) (Qualification, error) {
	if e := t.Validate(); e != nil {
		return Qualification{}, e
	}
	ordered := append([]Fact(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].At.Before(ordered[j].At)
	})
	deposit, bet := big.NewInt(0), big.NewInt(0)
	td, _ := Money(t.Deposit, true)
	tb, _ := Money(t.Turnover, true)
	out := Qualification{}
	seen := map[string]bool{}
	for _, f := range ordered {
		if !f.At.Before(cutoff) {
			continue
		}
		if seen[f.ID] {
			return out, errors.New("DUPLICATE_BUSINESS_EVENT")
		}
		seen[f.ID] = true
		if e := f.Validate(c, cutoff); e != nil {
			return out, e
		}
		if f.Kind == "DEPOSIT_SUCCEEDED" {
			n, _ := Money(f.Amount, true)
			deposit.Add(deposit, n)
		}
		if f.Kind == "BET_SETTLED" && c.AllowsVenue(f.VenueID) {
			n, _ := Money(f.ValidBet, false)
			bet.Add(bet, n)
		}
		if len(deposit.String()) > 38 || len(bet.String()) > 38 {
			return out, errors.New("AMOUNT_OUT_OF_RANGE")
		}
		if out.At == nil && deposit.Cmp(td) >= 0 && bet.Cmp(tb) >= 0 {
			v := f.At
			out.At = &v
		}
	}
	out.Deposit = deposit.String()
	out.Turnover = bet.String()
	return out, nil
}

type StepProgress struct {
	Step
	Completed          int        `json:"completed_members"`
	CumulativeRequired int        `json:"cumulative_required"`
	Unlocked           bool       `json:"unlocked"`
	QualifiedAt        *time.Time `json:"qualified_at"`
	Members            []string   `json:"qualified_user_ids"`
}
type QualifiedUser struct {
	UserID string
	At     time.Time
}

func (c Config) StepProgress(users []QualifiedUser) ([]StepProgress, error) {
	sorted := append([]QualifiedUser(nil), users...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].At.Equal(sorted[j].At) {
			return sorted[i].UserID < sorted[j].UserID
		}
		return sorted[i].At.Before(sorted[j].At)
	})
	seen := map[string]bool{}
	for _, u := range sorted {
		if !foundation.ValidID(u.UserID) || u.At.IsZero() || seen[u.UserID] {
			return nil, ErrValidation
		}
		seen[u.UserID] = true
	}
	out := []StepProgress{}
	offset := 0
	for _, s := range c.Steps {
		v := StepProgress{Step: s, CumulativeRequired: offset + s.Members, Members: []string{}}
		for i := offset; i < len(sorted) && i < offset+s.Members; i++ {
			v.Completed++
			v.Members = append(v.Members, sorted[i].UserID)
		}
		v.Unlocked = v.Completed == s.Members
		if v.Unlocked {
			at := sorted[offset+s.Members-1].At
			v.QualifiedAt = &at
		}
		out = append(out, v)
		offset += s.Members
	}
	return out, nil
}

type Contribution struct {
	UserID   string `json:"user_id"`
	Depth    int    `json:"depth"`
	Turnover string `json:"valid_bet_minor"`
	Rate     int    `json:"rate_ppm"`
	Amount   string `json:"commission_minor"`
}
type DailyCommission struct {
	RiskDecisions    []RiskDecision `json:"risk_decisions,omitempty"`
	UserID           string         `json:"user_id"`
	Day              time.Time      `json:"day"`
	Level            int            `json:"level"`
	QualifiedMembers int            `json:"qualified_members"`
	Turnover         string         `json:"valid_bet_minor"`
	Amount           string         `json:"commission_minor"`
	LayerTurnover    [3]string      `json:"layer_valid_bet_minor"`
	LayerAmounts     [3]string      `json:"layer_commission_minor"`
	Rates            [3]int         `json:"rates_ppm"`
	Contributions    []Contribution `json:"contributions"`
}

// 日终使用最终等级处理整日业务，物理邀请层级不压缩。
// Attribution 冻结单笔业务发生时的三层物理祖先；空位后不得再出现代理，不压缩停用或旧模块节点。
type Attribution struct {
	FactID    string    `json:"fact_id"`
	Ancestors [3]string `json:"ancestors"`
}

func CalculateDay(c Config, user string, day time.Time, regs []Registration, facts []Fact) (DailyCommission, error) {
	return calculateDay(c, user, day, regs, facts, nil, facts)
}

// CalculateAttributedDay 保留既有审批迁移的历史归属，日终最终等级不会把迁移前订单归给新上级。
func CalculateAttributedDay(c Config, user string, day time.Time, regs []Registration, facts []Fact, ownership []Attribution) (DailyCommission, error) {
	return calculateDay(c, user, day, regs, facts, &ownership, facts)
}

// CalculateRiskDay 在代理奖励开关筛选之外保留全站风控竞争事实，避免代理重开后各自选出不同赢家。
func CalculateRiskDay(c Config, user string, day time.Time, regs []Registration, facts []Fact, ownership []Attribution, riskFacts []Fact) (DailyCommission, error) {
	if ownership == nil {
		return calculateDay(c, user, day, regs, facts, nil, riskFacts)
	}
	return calculateDay(c, user, day, regs, facts, &ownership, riskFacts)
}
func calculateDay(c Config, user string, day time.Time, regs []Registration, facts []Fact, ownership *[]Attribution, riskFacts []Fact) (DailyCommission, error) {
	if e := c.Normalize(); e != nil {
		return DailyCommission{}, e
	}
	if !foundation.ValidID(user) {
		return DailyCommission{}, ErrValidation
	}
	start, end := c.Day(day)
	period, _ := c.Period(start)
	out := DailyCommission{UserID: user, Day: start, Turnover: "0", Amount: "0", Contributions: []Contribution{}}
	byID := map[string]Registration{}
	children := map[string][]string{}
	for _, r := range regs {
		if !foundation.ValidID(r.UserID) || r.UserID == r.ParentID || r.At.IsZero() {
			return out, ErrValidation
		}
		if _, ok := byID[r.UserID]; ok {
			return out, ErrValidation
		}
		byID[r.UserID] = r
		if r.At.Before(end) {
			children[r.ParentID] = append(children[r.ParentID], r.UserID)
		}
	}
	if _, ok := byID[user]; !ok {
		return out, errors.New("USER_NOT_FOUND")
	}
	for _, r := range regs {
		seen := map[string]bool{}
		p := r.UserID
		for p != "" {
			if seen[p] {
				return out, errors.New("RELATION_CYCLE")
			}
			seen[p] = true
			next, ok := byID[p]
			if !ok {
				return out, errors.New("RELATION_HISTORY_MISSING")
			}
			p = next.ParentID
		}
	}
	grouped := map[string][]Fact{}
	seenFacts := map[string]bool{}
	for _, f := range facts {
		if e := f.Validate(c, end); e != nil {
			return out, e
		}
		r, ok := byID[f.UserID]
		if !ok || f.At.Before(r.At) {
			return out, errors.New("RELATION_HISTORY_MISSING")
		}
		if seenFacts[f.ID] {
			return out, errors.New("DUPLICATE_BUSINESS_EVENT")
		}
		seenFacts[f.ID] = true
		grouped[f.UserID] = append(grouped[f.UserID], f)
	}
	for _, id := range children[user] {
		q, e := Qualify(c.GradeCondition, grouped[id], c, end)
		if e != nil {
			return out, e
		}
		if q.At != nil && !q.At.Before(period) {
			out.QualifiedMembers++
		}
	}
	depths := map[string]int{}
	front := []string{user}
	for depth := 1; depth <= 3; depth++ {
		next := []string{}
		for _, parent := range front {
			for _, id := range children[parent] {
				depths[id] = depth
				next = append(next, id)
			}
		}
		front = next
	}
	// 物理归属决定受益人范围；全站策略使用全站来源事实，不能按代理分别选出不同赢家。
	relevant := map[string]bool{}
	if ownership == nil {
		for _, f := range facts {
			_, ok := depths[f.UserID]
			relevant[f.ID] = ok
		}
	} else {
		for _, o := range *ownership {
			for _, a := range o.Ancestors {
				if a == user {
					relevant[o.FactID] = true
				}
			}
		}
	}
	excluded, decisions := CommissionRisk(c, riskFacts, relevant)
	out.RiskDecisions = decisions
	layers := [3]*big.Int{big.NewInt(0), big.NewInt(0), big.NewInt(0)}
	total := big.NewInt(0)
	perLayer := [3]map[string]*big.Int{{}, {}, {}}
	for id, depth := range depths {
		v := big.NewInt(0)
		for _, f := range grouped[id] {
			if !excluded[f.ID] && f.Kind == "BET_SETTLED" && !f.At.Before(start) && f.At.Before(end) && c.AllowsVenue(f.VenueID) {
				n, _ := Money(f.ValidBet, false)
				v.Add(v, n)
			}
		}
		perLayer[depth-1][id] = v
		layers[depth-1].Add(layers[depth-1], v)
		total.Add(total, v)
	}
	if ownership != nil {
		byFact := map[string][3]string{}
		for _, o := range *ownership {
			if _, ok := byFact[o.FactID]; ok {
				return out, ErrValidation
			}
			byFact[o.FactID] = o.Ancestors
		}
		layers = [3]*big.Int{big.NewInt(0), big.NewInt(0), big.NewInt(0)}
		total = big.NewInt(0)
		perLayer = [3]map[string]*big.Int{{}, {}, {}}
		for _, f := range facts {
			if excluded[f.ID] || f.Kind != "BET_SETTLED" || f.At.Before(start) || !f.At.Before(end) || !c.AllowsVenue(f.VenueID) {
				continue
			}
			ancestors, ok := byFact[f.ID]
			if !ok {
				return out, errors.New("RELATION_HISTORY_MISSING")
			}
			seen := map[string]bool{f.UserID: true}
			ended := false
			for n, ancestor := range ancestors {
				if ancestor == "" {
					ended = true
					continue
				}
				if ended || seen[ancestor] {
					return out, errors.New("RELATION_CYCLE")
				}
				seen[ancestor] = true
				if _, ok := byID[ancestor]; !ok {
					return out, errors.New("RELATION_HISTORY_MISSING")
				}
				if ancestor != user {
					continue
				}
				value, _ := Money(f.ValidBet, false)
				layers[n].Add(layers[n], value)
				total.Add(total, value)
				if perLayer[n][f.UserID] == nil {
					perLayer[n][f.UserID] = big.NewInt(0)
				}
				perLayer[n][f.UserID].Add(perLayer[n][f.UserID], value)
			}
		}
	}
	if len(total.String()) > 38 {
		return out, errors.New("AMOUNT_OUT_OF_RANGE")
	}
	out.Turnover = total.String()
	grade := CommissionGrade(c, out.QualifiedMembers, total)
	out.Level = grade.Number
	out.Rates = grade.Rates
	amount := big.NewInt(0)
	divisor := big.NewInt(1000000)
	for i, v := range layers {
		out.LayerTurnover[i] = v.String()
		n := CommissionAmount(v, grade.Rates[i])
		out.LayerAmounts[i] = n.String()
		amount.Add(amount, n)
	}
	if len(amount.String()) > 38 {
		return out, errors.New("AMOUNT_OUT_OF_RANGE")
	}
	out.Amount = amount.String()
	// 每层汇总后取整，再按最大余数分配给会员；同余数按会员ID排序，保证明细与应付佣金严格守恒。
	for depth := 1; depth <= 3; depth++ {
		type share struct {
			id     string
			n, rem *big.Int
		}
		parts := []share{}
		floorTotal := big.NewInt(0)
		for id, value := range perLayer[depth-1] {
			num := new(big.Int).Mul(value, big.NewInt(int64(grade.Rates[depth-1])))
			n, rem := new(big.Int), new(big.Int)
			n.QuoRem(num, divisor, rem)
			floorTotal.Add(floorTotal, n)
			parts = append(parts, share{id, n, rem})
		}
		sort.Slice(parts, func(i, j int) bool {
			cmp := parts[i].rem.Cmp(parts[j].rem)
			if cmp == 0 {
				return parts[i].id < parts[j].id
			}
			return cmp > 0
		})
		target, _ := Money(out.LayerAmounts[depth-1], false)
		extra := new(big.Int).Sub(target, floorTotal).Int64()
		for i, p := range parts {
			if int64(i) < extra {
				p.n.Add(p.n, big.NewInt(1))
			}
			out.Contributions = append(out.Contributions, Contribution{p.id, depth, perLayer[depth-1][p.id].String(), grade.Rates[depth-1], p.n.String()})
		}
	}
	sort.Slice(out.Contributions, func(i, j int) bool {
		a, _ := Money(out.Contributions[i].Amount, false)
		b, _ := Money(out.Contributions[j].Amount, false)
		cmp := a.Cmp(b)
		if cmp == 0 {
			if out.Contributions[i].UserID == out.Contributions[j].UserID {
				return out.Contributions[i].Depth < out.Contributions[j].Depth
			}
			return out.Contributions[i].UserID < out.Contributions[j].UserID
		}
		return cmp > 0
	})
	return out, nil
}
