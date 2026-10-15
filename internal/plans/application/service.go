package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/ports"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/plans/domain"
	"github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
)

func hash(body []byte) string {
	projected, err := foundation.BusinessCanonicalJSON(body)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(projected)
	return hex.EncodeToString(sum[:])
}

type Service struct {
	Catalog      ports.CurrencyCatalog
	Commands     db.Commands
	Contracts    agents.Contracts
	Participants agents.PlanParticipants
	Query        db.Executor
	Repo         ports.Repository
	Validator    ports.Validator
	Simulations  ports.Simulations
	Workflow     workflow.Repository
	Exponents    map[string]int
	Now          func() time.Time
	ID           func() string
}

func (s Service) Create(ctx context.Context, p identity.Principal, body []byte, key, request string) (foundation.CommandResult, error) {
	config, canonical, err := s.Validator.Decode(body)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	return s.Commands.Execute(ctx, foundation.Scope{Tenant: p.Tenant, App: p.App}, "plan-create", key, json.RawMessage(canonical), func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Create(ctx, tx, foundation.Scope{Tenant: p.Tenant, App: p.App}, config, canonical, hash(canonical), s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, config.PlanID, request, domain.Draft{Config: config, Revision: 0, Hash: hash(canonical), Status: "DRAFT"})
	})
}
func (s Service) Read(ctx context.Context, p identity.Principal, id string) (domain.Draft, error) {
	return s.Repo.Read(ctx, s.Query, foundation.Scope{Tenant: p.Tenant, App: p.App}, id, false)
}
func (s Service) Update(ctx context.Context, p identity.Principal, id string, raw []byte, expected uint64, key, request string) (foundation.CommandResult, error) {
	config, _, err := s.Validator.Decode(raw)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	if config.PlanID != id || config.Revision != expected {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	config.Revision++
	body, err := json.Marshal(config)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	_, canonical, err := s.Validator.Decode(body)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "plan-update", key, struct {
		Draft    json.RawMessage `json:"draft"`
		Expected uint64          `json:"expected_revision"`
	}{canonical, expected}, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Update(ctx, tx, scope, config, canonical, hash(canonical), expected, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, request, domain.Draft{Config: config, Revision: config.Revision, Hash: hash(canonical), Status: "DRAFT"})
	})
}

type SimulationRequest struct {
	DraftHash     *string                  `json:"draft_hash,omitempty"`
	VersionID     *string                  `json:"plan_version_id,omitempty"`
	Facts         calculation.Facts        `json:"business_facts"`
	Ancestors     []calculation.Actor      `json:"ancestors"`
	Qualification map[string]Qualification `json:"qualification_facts"`
}
type Qualification struct {
	Eligibility string     `json:"eligibility"`
	From        time.Time  `json:"effective_from"`
	To          *time.Time `json:"effective_to"`
	Status      string     `json:"status,omitempty"`
	GradeID     string     `json:"grade_id,omitempty"`
}

func (s Service) Simulate(ctx context.Context, p identity.Principal, input SimulationRequest, key, request string) (foundation.CommandResult, error) {
	input.Facts.BusinessTime = input.Facts.BusinessTime.UTC()
	for id, q := range input.Qualification {
		q.From = q.From.UTC()
		if q.To != nil {
			at := q.To.UTC()
			q.To = &at
		}
		input.Qualification[id] = q
	}
	if value, err := foundation.ParseDecimal(input.Facts.Basis); err == nil {
		input.Facts.Basis = value.String()
	}
	if input.Facts.Quantity != "" {
		if value, err := foundation.ParseDecimal(input.Facts.Quantity); err == nil {
			input.Facts.Quantity = value.String()
		}
	}
	if (input.DraftHash == nil) == (input.VersionID == nil) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "plan-simulate", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		var config domain.Config
		var configHash string
		if input.DraftHash != nil {
			draft, err := s.Simulations.ByHash(ctx, tx, scope, *input.DraftHash)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			config = draft.Config
			configHash = draft.Hash
		} else {
			version, err := s.Simulations.ByVersion(ctx, tx, scope, *input.VersionID)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			config = version.Config
			configHash = version.Hash
		}
		snapshot := calculation.Input{TaskID: "simulation", SnapshotID: s.ID(), Plan: config, Facts: input.Facts, Ancestors: input.Ancestors, Contracts: map[string]calculation.ContractFact{}, Exponent: s.Exponents[config.Currency]}
		snapshot.StandardVersion = "1"
		snapshot.ConfigHash = configHash
		snapshot.CurrencyCatalogVersion = "currency-v1"
		snapshot.CalendarVersion = config.CalendarVersion
		snapshot.PlanVersionID = "draft:" + config.PlanID
		if input.VersionID != nil {
			snapshot.PlanVersionID = *input.VersionID
		}
		snapshot.AlgorithmVersions = map[string]string{}
		for _, rule := range config.Rules {
			snapshot.AlgorithmVersions[rule.Algorithm] = rule.Version
		}
		if s.Catalog != nil {
			version, exponent, err := s.Catalog.CurrencyAt(ctx, tx, scope, config.Currency, input.Facts.BusinessTime)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			snapshot.CurrencyCatalogVersion = version
			snapshot.Exponent = exponent
		}
		snapshot.Recipients = []calculation.Actor{}
		snapshot.RelationshipFacts = []calculation.RelationshipEvidence{}
		snapshot.QualificationFacts = map[string]calculation.QualificationEvidence{}
		for id, q := range input.Qualification {
			snapshot.QualificationFacts[id] = calculation.QualificationEvidence{Joined: true, GradeID: q.GradeID, Eligibility: q.Eligibility, From: q.From, To: q.To, Source: "SIMULATION_INPUT", Evidence: "simulation:" + snapshot.SnapshotID}
		}
		// 模拟也要求显式资格历史；不把缺失事实补成当前状态。
		for _, actor := range input.Ancestors {
			qualification, ok := input.Qualification[actor.ID]
			if !ok || actor.Eligible == nil || qualification.From.IsZero() || (qualification.Eligibility != "ELIGIBLE" && qualification.Eligibility != "INELIGIBLE") || input.Facts.BusinessTime.Before(qualification.From) || (qualification.To != nil && !input.Facts.BusinessTime.Before(*qualification.To)) || (*actor.Eligible != (qualification.Eligibility == "ELIGIBLE")) {
				return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
			}
		}
		known := map[string]bool{}
		for _, actor := range input.Ancestors {
			known[actor.ID] = true
		}
		for _, rule := range config.Rules {
			if rule.Algorithm != "WEIGHTED" {
				continue
			}
			for _, candidate := range rule.Parameters.Beneficiaries {
				if known[candidate.BeneficiaryID] {
					continue
				}
				q, ok := input.Qualification[candidate.BeneficiaryID]
				if !ok || q.Status == "" || q.From.IsZero() || input.Facts.BusinessTime.Before(q.From) || (q.To != nil && !input.Facts.BusinessTime.Before(*q.To)) {
					return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
				}
				if q.Eligibility != "ELIGIBLE" && q.Eligibility != "INELIGIBLE" {
					return foundation.CommandResult{}, foundation.ErrValidation
				}
				eligible := q.Eligibility == "ELIGIBLE"
				snapshot.Recipients = append(snapshot.Recipients, calculation.Actor{ID: candidate.BeneficiaryID, Eligible: &eligible, Status: q.Status, Grade: q.GradeID, Depth: 0})
				known[candidate.BeneficiaryID] = true
			}
		}
		if config.AgentPolicy.RequireContract {
			if s.Contracts == nil {
				return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
			}
			contractActors := append(append([]calculation.Actor(nil), snapshot.Ancestors...), snapshot.Recipients...)
			for _, actor := range contractActors {
				contract, err := s.Contracts.ContractAt(ctx, tx, scope, actor.ID, config.PlanID, input.Facts.BusinessTime)
				if errors.Is(err, sql.ErrNoRows) {
					snapshot.Contracts[actor.ID] = calculation.ContractFact{}
					continue
				}
				if err != nil {
					return foundation.CommandResult{}, err
				}
				snapshot.Contracts[actor.ID] = calculation.ContractFact{PlanID: contract.PlanID, From: contract.From, To: contract.To}
			}
		}
		result := calculation.Calculate(snapshot)
		proofBody, err := json.Marshal(snapshot)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		proofBody, err = foundation.CanonicalJSON(proofBody)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		resultBody, err := json.Marshal(result)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		id := s.ID()
		proof := ports.Simulation{ID: id, PlanID: config.PlanID, ConfigHash: configHash, SnapshotHash: hash(proofBody), Snapshot: proofBody, Result: resultBody, Passed: result.Status == "SUCCEEDED" || result.Status == "NO_MATCH", At: s.Now()}
		if err = s.Simulations.Save(ctx, tx, scope, proof); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, request, map[string]any{"simulation_id": id, "snapshot_hash": proof.SnapshotHash, "result": result})
	})
}

type Publication struct {
	Revision      uint64    `json:"expected_revision"`
	DraftHash     string    `json:"draft_hash"`
	SimulationID  string    `json:"simulation_id"`
	EffectiveFrom time.Time `json:"effective_from"`
	Reason        string    `json:"reason"`
}
type PublicationPayload struct {
	PlanID        string    `json:"plan_id"`
	Revision      uint64    `json:"expected_revision"`
	Hash          string    `json:"draft_hash"`
	EffectiveFrom time.Time `json:"effective_from"`
}

func (s Service) RequestPublication(ctx context.Context, p identity.Principal, id string, input Publication, key, request string) (foundation.CommandResult, error) {
	if input.Reason == "" || input.EffectiveFrom.IsZero() || input.EffectiveFrom.Nanosecond()%1000 != 0 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	command := struct {
		PlanID      string      `json:"plan_id"`
		Publication Publication `json:"publication"`
	}{id, input}
	return s.Commands.Execute(ctx, scope, "plan-publication", key, command, func(tx db.Executor) (foundation.CommandResult, error) {
		draft, err := s.Repo.Read(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if draft.Revision != input.Revision || draft.Hash != input.DraftHash {
			return foundation.CommandResult{}, errors.New("STALE_APPROVAL")
		}
		for _, rule := range draft.Config.Rules {
			if calculation.Capabilities()[rule.Algorithm] != rule.Version {
				return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
			}
		}
		if err := s.validateParticipants(ctx, tx, scope, draft.Config, input.EffectiveFrom); err != nil {
			return foundation.CommandResult{}, err
		}

		proof, err := s.Simulations.Proof(ctx, tx, scope, input.SimulationID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if !proof.Passed || proof.PlanID != id || proof.ConfigHash != draft.Hash {
			return foundation.CommandResult{}, errors.New("STALE_APPROVAL")
		}
		if input.EffectiveFrom.Before(s.Now()) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		if err = s.Repo.SetPending(ctx, tx, scope, id, input.Revision, input.DraftHash); err != nil {
			return foundation.CommandResult{}, err
		}
		payload, err := json.Marshal(PublicationPayload{PlanID: id, Revision: input.Revision, Hash: input.DraftHash, EffectiveFrom: input.EffectiveFrom.UTC()})
		if err != nil {
			return foundation.CommandResult{}, err
		}
		payload, err = foundation.CanonicalJSON(payload)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		approvalID := s.ID()
		if err = s.Workflow.Create(ctx, tx, scope, approval.Request{ID: approvalID, Type: "PLAN_PUBLISH", Applicant: p.Subject, ObjectRef: id, ObjectHash: hash(payload), Payload: payload}, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, approvalID, request, map[string]string{"approval_id": approvalID})
	})
}

func (s Service) validateParticipants(ctx context.Context, tx db.Executor, scope foundation.Scope, config domain.Config, at time.Time) error {
	if s.Participants == nil {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	ids := append([]string(nil), config.Scope.AgentIDs...)
	for _, rule := range config.Rules {
		for _, beneficiary := range rule.Parameters.Beneficiaries {
			ids = append(ids, beneficiary.BeneficiaryID)
		}
	}
	for _, id := range ids {
		if _, err := s.Participants.Revision(ctx, tx, scope, id); err != nil {
			return err
		}
	}
	current, err := s.Participants.EligibleGrades(ctx, tx, scope, config.PlanID, at)
	if err != nil {
		return err
	}
	definitions := map[string]bool{}
	for _, grade := range config.Grades {
		definitions[grade.ID] = true
	}
	for _, grade := range current {
		if !definitions[grade] {
			return foundation.ErrValidation
		}
	}
	return nil
}

// ExecutePublication rechecks participants under the approval transaction, not only at request time.
func (s Service) ExecutePublication(ctx context.Context, tx db.Executor, scope foundation.Scope, input PublicationPayload, version string, at time.Time) error {
	draft, err := s.Repo.Read(ctx, tx, scope, input.PlanID, true)
	if err != nil {
		return err
	}
	if draft.Hash != input.Hash || draft.Revision != input.Revision {
		return errors.New("STALE_APPROVAL")
	}
	if err = s.validateParticipants(ctx, tx, scope, draft.Config, input.EffectiveFrom); err != nil {
		return err
	}
	return s.Repo.Publish(ctx, tx, scope, input.PlanID, input.Revision, input.Hash, version, input.EffectiveFrom, at)
}
