package domain

import (
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"sort"
	"time"
)

type AgentReportFilter struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	User       string    `json:"user_id"`
	Sort       string    `json:"sort"`
	Descending bool      `json:"descending"`
}

func (f AgentReportFilter) Validate() error {
	if f.From.IsZero() || !f.From.Before(f.To) || f.To.After(f.From.AddDate(1, 0, 0)) {
		return foundation.ErrValidation
	}
	switch f.Sort {
	case "became_agent_at", "team_valid_bet_minor", "direct_members", "effective_direct", "claimed_commission_minor":
	default:
		return foundation.ErrValidation
	}
	return nil
}

type TeamMember struct {
	Historical   bool       `json:"historical_relation"`
	Estimate     string     `json:"provisional_contribution_minor"`
	UserID       string     `json:"user_id"`
	Depth        int        `json:"depth"`
	RegisteredAt time.Time  `json:"registered_at"`
	QualifiedAt  *time.Time `json:"qualified_at"`
	Deposit      string     `json:"deposit_minor"`
	Withdrawal   string     `json:"withdrawal_minor"`
	ValidBet     string     `json:"valid_bet_minor"`
	Contribution string     `json:"contribution_minor"`
}
type AgentReportRow struct {
	Estimate             string       `json:"provisional_commission_minor"`
	UserID               string       `json:"user_id"`
	AgentID              string       `json:"agent_id"`
	RegisteredAt         time.Time    `json:"registered_at"`
	BecameAgentAt        time.Time    `json:"became_agent_at"`
	Enabled              bool         `json:"enabled"`
	GateRevision         uint64       `json:"gate_revision"`
	Direct               int          `json:"direct_members"`
	Team                 int          `json:"team_members"`
	EffectiveDirect      int          `json:"effective_direct"`
	EffectiveTeam        int          `json:"effective_team"`
	DirectDeposit        string       `json:"direct_deposit_minor"`
	TeamDeposit          string       `json:"team_deposit_minor"`
	DirectWithdrawal     string       `json:"direct_withdrawal_minor"`
	TeamWithdrawal       string       `json:"team_withdrawal_minor"`
	NetDeposit           string       `json:"net_deposit_minor"`
	DirectValidBet       string       `json:"direct_valid_bet_minor"`
	TeamValidBet         string       `json:"team_valid_bet_minor"`
	CalculatedCommission string       `json:"calculated_commission_minor"`
	ClaimedReferral      string       `json:"claimed_referral_minor"`
	ClaimedCommission    string       `json:"claimed_commission_minor"`
	Members              []TeamMember `json:"members"`
}
type AgentReport struct {
	Filter      AgentReportFilter `json:"filters"`
	Currency    string            `json:"currency"`
	Scale       int               `json:"currency_scale"`
	Timezone    string            `json:"timezone"`
	AsOf        time.Time         `json:"as_of"`
	Provisional bool              `json:"provisional"`
	Rows        []AgentReportRow  `json:"rows"`
}

func addMoney(a, b string) string {
	x, _ := new(big.Int).SetString(a, 10)
	y, _ := new(big.Int).SetString(b, 10)
	return x.Add(x, y).String()
}

// BuildAgentReport 把原始财务统计、等级资格与冻结佣金贡献分别显示；不把手动调整额伪装为会员计算贡献。
func BuildAgentReport(v affiliate.Version, f AgentReportFilter, asOf time.Time, subjects []Member, relations []affiliate.Registration, raw, eligible []affiliate.Fact, rewards []Reward, gates map[string]Gate, promoted map[string]time.Time, dailies []Daily) (AgentReport, error) {
	out := AgentReport{Filter: f, Currency: v.Config.Currency, Scale: v.Config.CurrencyScale, Timezone: v.Config.Timezone, AsOf: asOf, Rows: []AgentReportRow{}}
	if e := f.Validate(); e != nil {
		return out, e
	}
	if e := v.Config.Normalize(); e != nil {
		return out, e
	}
	_, endToday := v.Config.Day(asOf)
	startToday, _ := v.Config.Day(asOf)
	out.Provisional = f.To.After(startToday) && f.From.Before(endToday)
	cutoff := f.To
	if cutoff.After(asOf.Add(time.Microsecond)) {
		cutoff = asOf.Add(time.Microsecond)
	}
	byParent := map[string][]affiliate.Registration{}
	for _, r := range relations {
		if r.At.Before(cutoff) {
			byParent[r.ParentID] = append(byParent[r.ParentID], r)
		}
	}
	rawByUser := map[string][]affiliate.Fact{}
	for _, fact := range raw {
		rawByUser[fact.UserID] = append(rawByUser[fact.UserID], fact)
	}
	eligibleByUser := map[string][]affiliate.Fact{}
	eligibleIDs := map[string]bool{}
	for _, fact := range eligible {
		eligibleByUser[fact.UserID] = append(eligibleByUser[fact.UserID], fact)
		eligibleIDs[fact.ID] = true
	}
	period, _ := v.Config.Period(cutoff.Add(-time.Microsecond))
	for _, m := range subjects {
		if f.User != "" && f.User != m.UserID {
			continue
		}
		if len(byParent[m.UserID]) == 0 && promoted[m.UserID].IsZero() {
			continue
		}
		became := promoted[m.UserID]
		if became.IsZero() {
			became = byParent[m.UserID][0].At
			for _, c := range byParent[m.UserID] {
				if c.At.Before(became) {
					became = c.At
				}
			}
		}
		g := gates[m.UserID]
		row := AgentReportRow{UserID: m.UserID, AgentID: m.AgentID, RegisteredAt: m.RegisteredAt, BecameAgentAt: became, Enabled: g.Enabled, GateRevision: g.Revision, DirectDeposit: "0", TeamDeposit: "0", DirectWithdrawal: "0", TeamWithdrawal: "0", NetDeposit: "0", DirectValidBet: "0", TeamValidBet: "0", CalculatedCommission: "0", ClaimedReferral: "0", ClaimedCommission: "0", Estimate: "0", Members: []TeamMember{}}
		type contributionKey struct {
			user  string
			depth int
		}
		contribution := map[contributionKey]string{}
		for _, daily := range dailies {
			date, e := time.ParseInLocation("2006-01-02", daily.Day, v.Config.Location())
			if e != nil {
				return out, e
			}
			if date.Before(f.From) || !date.Before(f.To) {
				continue
			}
			for _, frozen := range daily.Results {
				if frozen.Result.UserID != m.UserID {
					continue
				}
				row.CalculatedCommission = addMoney(row.CalculatedCommission, frozen.Result.Amount)
				for _, c := range frozen.Result.Contributions {
					key := contributionKey{c.UserID, c.Depth}
					if contribution[key] == "" {
						contribution[key] = "0"
					}
					contribution[key] = addMoney(contribution[key], c.Amount)
				}
			}
		}
		front := []string{m.UserID}
		for depth := 1; depth <= 3; depth++ {
			next := []string{}
			for _, parent := range front {
				for _, child := range byParent[parent] {
					next = append(next, child.UserID)
					member := TeamMember{UserID: child.UserID, Depth: depth, RegisteredAt: child.At, Deposit: "0", Withdrawal: "0", ValidBet: "0", Contribution: "0", Estimate: "0"}
					selected := []affiliate.Fact{}
					for _, fact := range eligibleByUser[child.UserID] {
						if !fact.At.Before(g.Since) {
							selected = append(selected, fact)
						}
					}
					q, e := affiliate.Qualify(v.Config.GradeCondition, selected, v.Config, cutoff)
					if e != nil {
						return out, e
					}
					member.QualifiedAt = q.At
					effective := q.At != nil && !q.At.Before(period)
					row.Team++
					if effective {
						row.EffectiveTeam++
					}
					if depth == 1 {
						row.Direct++
						if effective {
							row.EffectiveDirect++
						}
					}
					for _, fact := range rawByUser[child.UserID] {
						if fact.At.Before(f.From) || !fact.At.Before(cutoff) {
							continue
						}
						switch fact.Kind {
						case "DEPOSIT_SUCCEEDED":
							member.Deposit = addMoney(member.Deposit, fact.Amount)
						case "WITHDRAWAL_SUCCEEDED":
							member.Withdrawal = addMoney(member.Withdrawal, fact.Amount)
						case "BET_SETTLED":
							if eligibleIDs[fact.ID] {
								member.ValidBet = addMoney(member.ValidBet, fact.ValidBet)
							}
						}
					}
					row.TeamDeposit = addMoney(row.TeamDeposit, member.Deposit)
					row.TeamWithdrawal = addMoney(row.TeamWithdrawal, member.Withdrawal)
					row.TeamValidBet = addMoney(row.TeamValidBet, member.ValidBet)
					if depth == 1 {
						row.DirectDeposit = addMoney(row.DirectDeposit, member.Deposit)
						row.DirectWithdrawal = addMoney(row.DirectWithdrawal, member.Withdrawal)
						row.DirectValidBet = addMoney(row.DirectValidBet, member.ValidBet)
					}
					key := contributionKey{child.UserID, depth}
					if contribution[key] != "" {
						member.Contribution = contribution[key]
						delete(contribution, key)
					}
					row.Members = append(row.Members, member)
				}
			}
			front = next
		}
		// 迁移后的当前团队不包含旧贡献者，仍单独保留其业务发生时的层级；不重复计入当前人数和充值统计。
		for key, value := range contribution {
			registered := time.Time{}
			for _, subject := range subjects {
				if subject.UserID == key.user {
					registered = subject.RegisteredAt
					break
				}
			}
			row.Members = append(row.Members, TeamMember{Historical: true, UserID: key.user, Depth: key.depth, RegisteredAt: registered, Deposit: "0", Withdrawal: "0", ValidBet: "0", Contribution: value, Estimate: "0"})
		}
		deposit, _ := new(big.Int).SetString(row.TeamDeposit, 10)
		withdrawal, _ := new(big.Int).SetString(row.TeamWithdrawal, 10)
		row.NetDeposit = deposit.Sub(deposit, withdrawal).String()
		for _, r := range rewards {
			if r.UserID != m.UserID || r.Status != "CLAIMED" || r.ClaimedAt == nil || r.ClaimedAt.Before(f.From) || !r.ClaimedAt.Before(cutoff) {
				continue
			}
			switch r.Kind {
			case Referral:
				row.ClaimedReferral = addMoney(row.ClaimedReferral, r.Amount)
			case Commission:
				row.ClaimedCommission = addMoney(row.ClaimedCommission, r.Amount)
			}
		}
		sort.Slice(row.Members, func(i, j int) bool {
			if row.Members[i].Depth == row.Members[j].Depth {
				return row.Members[i].UserID < row.Members[j].UserID
			}
			return row.Members[i].Depth < row.Members[j].Depth
		})
		out.Rows = append(out.Rows, row)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		compare := 0
		switch f.Sort {
		case "became_agent_at":
			if a.BecameAgentAt.Before(b.BecameAgentAt) {
				compare = -1
			} else if a.BecameAgentAt.After(b.BecameAgentAt) {
				compare = 1
			}
		case "direct_members":
			compare = a.Direct - b.Direct
		case "effective_direct":
			compare = a.EffectiveDirect - b.EffectiveDirect
		default:
			x, y := a.TeamValidBet, b.TeamValidBet
			if f.Sort == "claimed_commission_minor" {
				x, y = a.ClaimedCommission, b.ClaimedCommission
			}
			n, _ := new(big.Int).SetString(x, 10)
			m, _ := new(big.Int).SetString(y, 10)
			compare = n.Cmp(m)
		}
		if compare == 0 {
			return a.UserID < b.UserID
		}
		if f.Descending {
			return compare > 0
		}
		return compare < 0
	})
	return out, nil
}

type AgentSummary struct {
	Agents            int    `json:"agents"`
	Direct            int    `json:"direct_members"`
	Team              int    `json:"team_members"`
	DirectDeposit     string `json:"direct_deposit_minor"`
	TeamDeposit       string `json:"team_deposit_minor"`
	TeamWithdrawal    string `json:"team_withdrawal_minor"`
	NetDeposit        string `json:"net_deposit_minor"`
	ValidBet          string `json:"team_valid_bet_minor"`
	Calculated        string `json:"calculated_commission_minor"`
	ClaimedReferral   string `json:"claimed_referral_minor"`
	ClaimedCommission string `json:"claimed_commission_minor"`
	Estimate          string `json:"provisional_commission_minor"`
}

func SummarizeAgents(rows []AgentReportRow) AgentSummary {
	s := AgentSummary{Agents: len(rows), DirectDeposit: "0", TeamDeposit: "0", TeamWithdrawal: "0", NetDeposit: "0", ValidBet: "0", Calculated: "0", ClaimedReferral: "0", ClaimedCommission: "0", Estimate: "0"}
	for _, r := range rows {
		s.Direct += r.Direct
		s.Team += r.Team
		s.DirectDeposit = addMoney(s.DirectDeposit, r.DirectDeposit)
		s.TeamDeposit = addMoney(s.TeamDeposit, r.TeamDeposit)
		s.TeamWithdrawal = addMoney(s.TeamWithdrawal, r.TeamWithdrawal)
		s.NetDeposit = addMoney(s.NetDeposit, r.NetDeposit)
		s.ValidBet = addMoney(s.ValidBet, r.TeamValidBet)
		s.Calculated = addMoney(s.Calculated, r.CalculatedCommission)
		s.ClaimedReferral = addMoney(s.ClaimedReferral, r.ClaimedReferral)
		s.ClaimedCommission = addMoney(s.ClaimedCommission, r.ClaimedCommission)
		s.Estimate = addMoney(s.Estimate, r.Estimate)
	}
	return s
}

type AgentPage struct {
	Snapshot    string            `json:"snapshot_id"`
	Filters     AgentReportFilter `json:"filters"`
	AsOf        time.Time         `json:"as_of"`
	Currency    string            `json:"currency"`
	Scale       int               `json:"currency_scale"`
	Timezone    string            `json:"timezone"`
	Provisional bool              `json:"provisional"`
	Rows        []AgentReportRow  `json:"rows"`
	Summary     AgentSummary      `json:"summary"`
	PageSummary AgentSummary      `json:"page_summary"`
	Next        *int              `json:"next_offset"`
}

func AgentReportPage(r AgentReport, id string, offset, limit int) (AgentPage, error) {
	out := AgentPage{Snapshot: id, Filters: r.Filter, AsOf: r.AsOf, Currency: r.Currency, Scale: r.Scale, Timezone: r.Timezone, Provisional: r.Provisional, Summary: SummarizeAgents(r.Rows)}
	if offset < 0 || offset > len(r.Rows) || limit < 1 || limit > 100 {
		return out, foundation.ErrValidation
	}
	end := offset + limit
	if end > len(r.Rows) {
		end = len(r.Rows)
	}
	out.Rows = append([]AgentReportRow{}, r.Rows[offset:end]...)
	out.PageSummary = SummarizeAgents(out.Rows)
	if end < len(r.Rows) {
		out.Next = &end
	}
	return out, nil
}

// AddProvisionalContribution 沿用业务发生时的受益层级展示预估贡献，不能把同一会员不同层级的金额重复加到每行。
func AddProvisionalContribution(row *AgentReportRow, user string, depth int, value string) {
	for i := range row.Members {
		if row.Members[i].UserID == user && row.Members[i].Depth == depth {
			row.Members[i].Estimate = addMoney(row.Members[i].Estimate, value)
			return
		}
	}
	row.Members = append(row.Members, TeamMember{Historical: true, UserID: user, Depth: depth, Deposit: "0", Withdrawal: "0", ValidBet: "0", Contribution: "0", Estimate: value})
	sort.Slice(row.Members, func(i, j int) bool {
		if row.Members[i].Depth == row.Members[j].Depth {
			return row.Members[i].UserID < row.Members[j].UserID
		}
		return row.Members[i].Depth < row.Members[j].Depth
	})
}
