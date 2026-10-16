package adapters

import (
	"context"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/wallet/domain"
	"math/big"
	"time"
)

type Repository struct{}

func (Repository) Credit(ctx context.Context, tx db.Executor, s foundation.Scope, c model.Credit, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_accounts VALUES(?,?,?,?,0,0) ON DUPLICATE KEY UPDATE user_id=VALUES(user_id)`, s.Tenant, s.App, c.UserID, c.Currency); err != nil {
		return err
	}
	var before string
	if err := tx.QueryRowContext(ctx, `SELECT CAST(balance AS CHAR) FROM wallet_accounts WHERE tenant_id=? AND app_id=? AND user_id=? AND currency=? FOR UPDATE`, s.Tenant, s.App, c.UserID, c.Currency).Scan(&before); err != nil {
		return err
	}
	var old model.Credit
	err := tx.QueryRowContext(ctx, `SELECT user_id,currency,CAST(amount AS CHAR),business_ref,payment_id,statement_id FROM wallet_transactions WHERE tenant_id=? AND app_id=? AND business_ref=?`, s.Tenant, s.App, c.Reference).Scan(&old.UserID, &old.Currency, &old.Amount, &old.Reference, &old.PaymentID, &old.StatementID)
	if err == nil {
		if old != c {
			return foundation.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	a, ok := new(big.Int).SetString(before, 10)
	if !ok {
		return errors.New("UNKNOWN_FACT")
	}
	n, _ := new(big.Int).SetString(c.Amount, 10)
	after := a.Add(a, n).String()
	if len(after) > 38 {
		return errors.New("AMOUNT_OUT_OF_RANGE")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO wallet_transactions VALUES(?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, c.Reference, c.UserID, c.Currency, c.Amount, c.Reference, c.PaymentID, c.StatementID, at); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO wallet_changes VALUES(?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, c.Reference, c.UserID, c.Currency, c.Amount, before, after, at); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE wallet_accounts SET balance=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND user_id=? AND currency=?`, after, s.Tenant, s.App, c.UserID, c.Currency)
	return err
}
func (Repository) Accounts(ctx context.Context, tx db.Executor, s foundation.Scope, user string) ([]model.Account, error) {
	rows, err := tx.QueryContext(ctx, `SELECT currency,CAST(balance AS CHAR),revision FROM wallet_accounts WHERE tenant_id=? AND app_id=? AND user_id=? ORDER BY currency`, s.Tenant, s.App, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Account{}
	for rows.Next() {
		v := model.Account{Type: "REAL_MONEY"}
		if err = rows.Scan(&v.Currency, &v.Balance, &v.Revision); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (Repository) Entries(ctx context.Context, tx db.Executor, s foundation.Scope, user, currency, after string, limit int, changes bool) ([]model.Entry, error) {
	q := `SELECT transaction_id,currency,CAST(amount AS CHAR),payment_id,statement_id,created_at FROM wallet_transactions WHERE tenant_id=? AND app_id=? AND user_id=? AND (?='' OR currency=?) AND transaction_id>? ORDER BY transaction_id LIMIT ?`
	if changes {
		q = `SELECT change_id,currency,CAST(amount AS CHAR),CAST(balance_before AS CHAR),CAST(balance_after AS CHAR),created_at FROM wallet_changes WHERE tenant_id=? AND app_id=? AND user_id=? AND (?='' OR currency=?) AND change_id>? ORDER BY change_id LIMIT ?`
	}
	rows, err := tx.QueryContext(ctx, q, s.Tenant, s.App, user, currency, currency, after, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Entry{}
	for rows.Next() {
		var e model.Entry
		if changes {
			err = rows.Scan(&e.ID, &e.Currency, &e.Amount, &e.Before, &e.After, &e.Created)
		} else {
			err = rows.Scan(&e.ID, &e.Currency, &e.Amount, &e.PaymentID, &e.StatementID, &e.Created)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
