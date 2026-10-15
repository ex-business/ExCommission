package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/ports"
	"github.com/ex-business/ExCommission/internal/calculation/domain"
	ports "github.com/ex-business/ExCommission/internal/calculation/ports"
	event "github.com/ex-business/ExCommission/internal/integration/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/ports"
	"time"
)

type Resolver struct {
	Catalog       ports.CurrencyCatalog
	Plans         plans.Repository
	Users         users.BusinessUsers
	Accounts      agents.HistoricalAccounts
	Relationships agents.Repository
	Grades        agents.Grades
	Contracts     agents.Contracts
	Exponents     map[string]int
}

func (r Resolver) Resolve(ctx context.Context, tx db.Executor, s foundation.Scope, task domain.Task) (domain.Input, error) {
	var source event.Event
	if err := json.Unmarshal(task.EventBody, &source); err != nil {
		return domain.Input{}, errors.New("SNAPSHOT_INTEGRITY_FAILED")
	}
	version, err := r.Plans.Select(ctx, tx, s, plan.Route{Product: source.Product, EventType: source.Type, Scene: source.Scene}, source.Time)
	if err != nil {
		return domain.Input{}, err
	}
	exponent, ok := r.Exponents[version.Config.Currency]
	if !ok {
		return domain.Input{}, errors.New("UNSUPPORTED_CAPABILITY")
	}
	catalogVersion := "currency-v1"
	if r.Catalog != nil {
		catalogVersion, exponent, err = r.Catalog.CurrencyAt(ctx, tx, s, version.Config.Currency, task.BusinessTime)
		if err != nil {
			return domain.Input{}, err
		}
	}
	snapshot := domain.Input{RelationshipFacts: []domain.RelationshipEvidence{}, TaskID: task.ID, SnapshotID: "snapshot_" + task.ID, StandardVersion: "1", PlanVersionID: version.ID, ConfigHash: version.Hash, CurrencyCatalogVersion: catalogVersion, CalendarVersion: version.Config.CalendarVersion, AlgorithmVersions: map[string]string{}, Plan: version.Config, Facts: domain.Facts{Basis: source.Payload.Basis, Currency: source.Payload.Currency, Quantity: source.Payload.Quantity, QuantityUnit: source.Payload.QuantityUnit, Product: source.Product, Scene: source.Scene, Confirmed: source.Payload.Confirmed, BusinessTime: source.Time}, Ancestors: []domain.Actor{}, Recipients: []domain.Actor{}, Contracts: map[string]domain.ContractFact{}, Exponent: exponent}
	for _, rule := range version.Config.Rules {
		snapshot.AlgorithmVersions[rule.Algorithm] = rule.Version
	}
	user, err := r.Users.Resolve(ctx, tx, s, source.Source, source.ExternalUser)
	if err != nil {
		return snapshot, err
	}
	own, isAgent, err := r.Accounts.ByUserAt(ctx, tx, s, user, source.Time)
	if err != nil {
		return snapshot, err
	}
	if isAgent {
		ancestors, more, err := r.Relationships.Ancestors(ctx, tx, s, own.ID, source.Time, 0, version.Config.AgentPolicy.MaxDepth)
		if err != nil {
			return snapshot, err
		}
		snapshot.DepthLimitReached = more
		for _, ancestor := range ancestors {
			snapshot.RelationshipFacts = append(snapshot.RelationshipFacts, domain.RelationshipEvidence{AgentID: ancestor.AgentID, Depth: ancestor.Depth, From: ancestor.EffectiveFrom, To: ancestor.EffectiveTo, Kind: "AGENT_RELATION"})
			actor, err := r.actor(ctx, tx, s, version.Config, ancestor.AgentID, ancestor.Depth, source.Time)
			if err != nil {
				return snapshot, err
			}
			snapshot.Ancestors = append(snapshot.Ancestors, actor)
		}
	} else {
		binding, err := r.Users.BindingAt(ctx, tx, s, user, source.Time)
		if err != nil {
			return snapshot, err
		}
		direct := binding.AgentID
		snapshot.RelationshipFacts = append(snapshot.RelationshipFacts, domain.RelationshipEvidence{AgentID: direct, Depth: 1, From: binding.From, To: binding.To, Kind: "USER_BINDING", Evidence: binding.Evidence})
		actor, err := r.actor(ctx, tx, s, version.Config, direct, 1, source.Time)
		if err != nil {
			return snapshot, err
		}
		snapshot.Ancestors = append(snapshot.Ancestors, actor)
		parents, more, err := r.Relationships.Ancestors(ctx, tx, s, direct, source.Time, 0, version.Config.AgentPolicy.MaxDepth)
		if err != nil {
			return snapshot, err
		}
		snapshot.DepthLimitReached = more || len(parents)+1 > version.Config.AgentPolicy.MaxDepth
		for _, parent := range parents {
			if parent.Depth+1 > version.Config.AgentPolicy.MaxDepth {
				break
			}
			snapshot.RelationshipFacts = append(snapshot.RelationshipFacts, domain.RelationshipEvidence{AgentID: parent.AgentID, Depth: parent.Depth + 1, From: parent.EffectiveFrom, To: parent.EffectiveTo, Kind: "AGENT_RELATION"})
			actor, err := r.actor(ctx, tx, s, version.Config, parent.AgentID, parent.Depth+1, source.Time)
			if err != nil {
				return snapshot, err
			}
			snapshot.Ancestors = append(snapshot.Ancestors, actor)
		}
	}
	known := map[string]bool{}
	for _, actor := range snapshot.Ancestors {
		known[actor.ID] = true
	}
	for _, rule := range version.Config.Rules {
		if rule.Algorithm != "WEIGHTED" {
			continue
		}
		for _, candidate := range rule.Parameters.Beneficiaries {
			if known[candidate.BeneficiaryID] {
				continue
			}
			actor, err := r.actor(ctx, tx, s, version.Config, candidate.BeneficiaryID, 0, source.Time)
			if err != nil {
				return snapshot, err
			}
			snapshot.Recipients = append(snapshot.Recipients, actor)
			known[actor.ID] = true
		}
	}
	snapshot.QualificationFacts = map[string]domain.QualificationEvidence{}
	for _, a := range append(append([]domain.Actor{}, snapshot.Ancestors...), snapshot.Recipients...) {
		if a.Qualification != nil {
			snapshot.QualificationFacts[a.ID] = *a.Qualification
		}
	}
	if version.Config.AgentPolicy.RequireContract {
		all := append(append([]domain.Actor(nil), snapshot.Ancestors...), snapshot.Recipients...)
		for _, actor := range all {
			contract, err := r.Contracts.ContractAt(ctx, tx, s, actor.ID, version.Config.PlanID, source.Time)
			if errors.Is(err, sql.ErrNoRows) {
				snapshot.Contracts[actor.ID] = domain.ContractFact{}
				continue
			}
			if err != nil {
				return snapshot, err
			}
			snapshot.Contracts[actor.ID] = domain.ContractFact{PlanID: contract.PlanID, From: contract.From, To: contract.To}
		}
	}
	return snapshot, nil
}
func (r Resolver) actor(ctx context.Context, tx db.Executor, s foundation.Scope, config plan.Config, id string, depth int, at time.Time) (domain.Actor, error) {
	account, err := r.Accounts.AccountAt(ctx, tx, s, id, at)
	if err != nil {
		return domain.Actor{}, err
	}
	grade, found, err := r.Grades.GradeAt(ctx, tx, s, id, config.PlanID, at)
	if err != nil {
		return domain.Actor{}, err
	}
	if !found {
		for _, definition := range config.Grades {
			if definition.Rank == 0 {
				grade.Grade = definition.ID
			}
		}
	}
	eligible := grade.Eligibility == "ELIGIBLE"
	return domain.Actor{ID: id, Depth: depth, Grade: grade.Grade, Eligible: &eligible, Status: account.Status, Qualification: &domain.QualificationEvidence{Joined: found, GradeID: grade.Grade, Eligibility: grade.Eligibility, Revision: grade.Revision, From: grade.From, To: grade.To, Source: grade.Source, Evidence: grade.Evidence, StatusFrom: account.StatusFrom, StatusTo: account.StatusTo}}, nil
}
