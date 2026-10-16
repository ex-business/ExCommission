package adapters

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Review struct {
	ID                string `json:"approval_id"`
	Applicant         string `json:"applicant_id"`
	Status            string `json:"approval_status"`
	Execution         string `json:"execution_status"`
	Hash              string `json:"object_hash"`
	Revision          uint64 `json:"revision"`
	StatementID       string `json:"statement_id"`
	StatementRevision uint64 `json:"statement_revision"`
	StatementStatus   string `json:"statement_status"`
	Agent             string `json:"agent_id"`
	Currency          string `json:"currency"`
	Period            string `json:"period"`
	Total             string `json:"total_minor"`
	Remark            string `json:"remark"`
}
type Filter struct {
	Status, Agent, Currency, Period, After, Statement string
	Limit                                             int
}

func (Repository) Reviews(ctx context.Context, tx db.Executor, s foundation.Scope, f Filter) ([]Review, error) {
	rows, err := tx.QueryContext(ctx, `SELECT w.approval_id,w.applicant_id,w.status,w.execution_status,w.object_hash,w.revision,s.statement_id,s.revision,s.status,s.beneficiary_id,s.currency,s.period,CAST(s.total AS CHAR),COALESCE((SELECT n.note FROM workflow_decision_notes n WHERE n.tenant_id=w.tenant_id AND n.app_id=w.app_id AND n.approval_id=w.approval_id ORDER BY n.revision DESC LIMIT 1),'') FROM workflow_requests w JOIN settlement_statements s ON s.tenant_id=w.tenant_id AND s.app_id=w.app_id AND s.statement_id=w.object_ref WHERE w.tenant_id=? AND w.app_id=? AND w.request_type='STATEMENT' AND (?='' OR w.status=?) AND (?='' OR s.beneficiary_id=?) AND (?='' OR s.currency=?) AND (?='' OR s.period=?) AND w.approval_id>? ORDER BY w.approval_id LIMIT ?`, s.Tenant, s.App, f.Status, f.Status, f.Agent, f.Agent, f.Currency, f.Currency, f.Period, f.Period, f.After, f.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		var r Review
		if err = rows.Scan(&r.ID, &r.Applicant, &r.Status, &r.Execution, &r.Hash, &r.Revision, &r.StatementID, &r.StatementRevision, &r.StatementStatus, &r.Agent, &r.Currency, &r.Period, &r.Total, &r.Remark); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (r Repository) Instructions(ctx context.Context, tx db.Executor, s foundation.Scope, f Filter) ([]model.Instruction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT instruction_id FROM payout_instructions WHERE tenant_id=? AND app_id=? AND (?='' OR status=?) AND (?='' OR statement_id=?) AND instruction_id>? ORDER BY instruction_id LIMIT ?`, s.Tenant, s.App, f.Status, f.Status, f.Statement, f.Statement, f.After, f.Limit+1)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.Instruction{}
	for _, id := range ids {
		i, e := r.Get(ctx, tx, s, id, false)
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, nil
}
