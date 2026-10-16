package adapters

import (
	"context"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type MemberStatement struct {
	ID           string        `json:"statement_id"`
	Period       string        `json:"period"`
	Currency     string        `json:"currency"`
	Total        string        `json:"total_minor"`
	ReviewStatus string        `json:"review_status"`
	Orders       []MemberOrder `json:"orders"`
}
type MemberOrder struct {
	ID       string `json:"instruction_id"`
	Amount   string `json:"amount_minor"`
	Revision uint64 `json:"revision"`
	Status   string `json:"claim_status"`
}

func (Repository) MemberStatements(ctx context.Context, tx db.Executor, s foundation.Scope, user, after string, limit int) ([]MemberStatement, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.statement_id,s.period,s.currency,CAST(s.total AS CHAR),COALESCE((SELECT w.status FROM workflow_requests w WHERE w.tenant_id=s.tenant_id AND w.app_id=s.app_id AND w.object_ref=s.statement_id AND w.request_type='STATEMENT' ORDER BY w.created_at DESC,w.approval_id DESC LIMIT 1),'DRAFT') FROM settlement_statements s JOIN agent_accounts a ON a.tenant_id=s.tenant_id AND a.app_id=s.app_id AND a.agent_id=s.beneficiary_id WHERE s.tenant_id=? AND s.app_id=? AND a.user_id=? AND s.statement_id>? ORDER BY s.statement_id LIMIT ?`, s.Tenant, s.App, user, after, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberStatement{}
	for rows.Next() {
		var v MemberStatement
		if err = rows.Scan(&v.ID, &v.Period, &v.Currency, &v.Total, &v.ReviewStatus); err != nil {
			return nil, err
		}
		v.Orders = []MemberOrder{}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (Repository) MemberOrders(ctx context.Context, tx db.Executor, s foundation.Scope, statement, user string) ([]MemberOrder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.body,COALESCE(p.status,''),COALESCE(p.reconciliation_status,'') FROM payout_instructions i LEFT JOIN payment_orders p ON p.tenant_id=i.tenant_id AND p.app_id=i.app_id AND p.payment_id=JSON_UNQUOTE(JSON_EXTRACT(CONVERT(i.body USING utf8mb4),'$.payment_id')) WHERE i.tenant_id=? AND i.app_id=? AND i.statement_id=? ORDER BY i.created_at,i.instruction_id`, s.Tenant, s.App, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberOrder{}
	for rows.Next() {
		var body []byte
		var payment, recon string
		if err = rows.Scan(&body, &payment, &recon); err != nil {
			return nil, err
		}
		i, err := decodeInstruction(body)
		if err != nil {
			return nil, err
		}
		if i.DeliveryMode == "MEMBER_CLAIM" && i.BeneficiaryUserID == user {
			out = append(out, MemberOrder{ID: i.ID, Amount: i.Amount.Minor, Revision: i.Revision, Status: model.ClaimState(i.Status, payment, recon)})
		}
	}
	return out, rows.Err()
}

func decodeInstruction(body []byte) (model.Instruction, error) {
	var i model.Instruction
	err := json.Unmarshal(body, &i)
	return i, err
}
