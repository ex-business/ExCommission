package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/plans/domain"
	"github.com/ex-business/ExCommission/internal/plans/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Simulations struct{}

func (Simulations) Save(ctx context.Context, tx db.Executor, s foundation.Scope, proof ports.Simulation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO plan_simulations VALUES(?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, proof.ID, proof.PlanID, proof.ConfigHash, proof.SnapshotHash, proof.Snapshot, proof.Result, proof.Passed, proof.At.UTC())
	return err
}
func (Simulations) Proof(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (ports.Simulation, error) {
	var proof ports.Simulation
	err := tx.QueryRowContext(ctx, `SELECT simulation_id,plan_id,config_hash,snapshot_hash,snapshot,result,passed,created_at FROM plan_simulations WHERE tenant_id=? AND app_id=? AND simulation_id=?`, s.Tenant, s.App, id).Scan(&proof.ID, &proof.PlanID, &proof.ConfigHash, &proof.SnapshotHash, &proof.Snapshot, &proof.Result, &proof.Passed, &proof.At)
	return proof, err
}
func (Simulations) ByHash(ctx context.Context, tx db.Executor, s foundation.Scope, hash string) (domain.Draft, error) {
	var draft domain.Draft
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT config,revision,config_hash,status FROM plan_drafts WHERE tenant_id=? AND app_id=? AND config_hash=?`, s.Tenant, s.App, hash).Scan(&body, &draft.Revision, &draft.Hash, &draft.Status)
	if err != nil {
		return draft, err
	}
	err = json.Unmarshal(body, &draft.Config)
	return draft, err
}
func (Simulations) ByVersion(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (domain.Version, error) {
	var version domain.Version
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT version_id,version_number,config,config_hash,effective_from,effective_to FROM plan_versions WHERE tenant_id=? AND app_id=? AND version_id=?`, s.Tenant, s.App, id).Scan(&version.ID, &version.Number, &body, &version.Hash, &version.From, &version.To)
	if err != nil {
		return version, err
	}
	err = json.Unmarshal(body, &version.Config)
	if version.ID == "" {
		return version, errors.New("NOT_FOUND")
	}
	return version, err
}

func (s Simulations) LatestPassed(ctx context.Context, tx db.Executor, scope foundation.Scope, hash string) (ports.Simulation, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT simulation_id FROM plan_simulations WHERE tenant_id=? AND app_id=? AND config_hash=? AND passed=1 ORDER BY created_at DESC,simulation_id DESC LIMIT 1`, scope.Tenant, scope.App, hash).Scan(&id)
	if err != nil {
		return ports.Simulation{}, err
	}
	return s.Proof(ctx, tx, scope, id)
}
