package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/directory/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Save(ctx context.Context, tx db.Executor, s foundation.Scope, in model.Statistics) error {
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM users_external WHERE tenant_id=? AND app_id=? AND user_id=? FOR UPDATE`, s.Tenant, s.App, in.UserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("NOT_FOUND")
		}
		return err
	}
	var old []byte
	body, _ := json.Marshal(in)
	err := tx.QueryRowContext(ctx, `SELECT body FROM member_statistics WHERE tenant_id=? AND app_id=? AND user_id=? AND currency=? AND observed_at=?`, s.Tenant, s.App, in.UserID, in.Currency, in.Observed).Scan(&old)
	if err == nil {
		if string(old) != string(body) {
			return foundation.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO member_statistics VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, in.UserID, in.Currency, in.Observed, body)
	return err
}
func (Repository) Rows(ctx context.Context, tx db.Executor, s foundation.Scope, root, currency string, at time.Time, depth int) ([]model.Row, error) {
	// Historical edges preserve physical levels. Both agent children and ordinary bound members are included.
	q := `WITH RECURSIVE tree AS (
 SELECT agent_id,0 AS depth FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=? AND created_at<=?
 UNION ALL SELECT r.agent_id,t.depth+1 FROM agent_relations r JOIN tree t ON r.parent_id=t.agent_id WHERE r.tenant_id=? AND r.app_id=? AND r.effective_from<=? AND (r.effective_to IS NULL OR r.effective_to>?) AND t.depth<3
 ), people AS (
 SELECT a.user_id,a.agent_id,a.display_name,a.status,t.depth FROM agent_accounts a JOIN tree t ON t.agent_id=a.agent_id WHERE a.tenant_id=? AND a.app_id=? AND t.depth>0
 UNION ALL SELECT b.user_id,NULL AS agent_id,u.display_name,'MEMBER' AS status,t.depth+1 FROM user_agent_bindings b JOIN tree t ON t.agent_id=b.agent_id JOIN users_external u ON u.tenant_id=b.tenant_id AND u.app_id=b.app_id AND u.user_id=b.user_id WHERE b.tenant_id=? AND b.app_id=? AND b.effective_from<=? AND (b.effective_to IS NULL OR b.effective_to>?) AND t.depth<3 AND NOT EXISTS (SELECT 1 FROM agent_accounts a WHERE a.tenant_id=b.tenant_id AND a.app_id=b.app_id AND a.user_id=b.user_id)
 ) SELECT user_id,agent_id,display_name,status,depth FROM people WHERE (?=0 OR depth=?) ORDER BY user_id`
	args := []any{s.Tenant, s.App, root, at, s.Tenant, s.App, at, at, s.Tenant, s.App, s.Tenant, s.App, at, at, depth, depth}
	if root == "" {
		q = `SELECT user_id,agent_id,display_name,status,0 AS depth FROM agent_accounts WHERE tenant_id=? AND app_id=? AND created_at<=? ORDER BY user_id`
		args = []any{s.Tenant, s.App, at}
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := []model.Row{}
	for rows.Next() {
		var r model.Row
		if err = rows.Scan(&r.UserID, &r.AgentID, &r.Name, &r.Status, &r.Depth); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for j := range out {
		var body []byte
		err = tx.QueryRowContext(ctx, `SELECT body FROM member_statistics WHERE tenant_id=? AND app_id=? AND user_id=? AND currency=? AND observed_at<=? ORDER BY observed_at DESC LIMIT 1`, s.Tenant, s.App, out[j].UserID, currency, at).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var v model.Statistics
		if err = json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		out[j].Statistics = &v
	}
	return out, nil
}
