package adapters

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	driver "github.com/go-sql-driver/mysql"
	"math/big"
	"strconv"
	"time"
)

// AgentInvitation 优先提供新格式分享码，旧凭证仍由 Invitation 原样识别，不迁移历史注册归属。
func (Repository) AgentInvitation(ctx context.Context, tx db.Executor, s foundation.Scope, source, agent string) (model.Invitation, error) {
	i := model.Invitation{SourceID: source, AgentID: agent}
	e := tx.QueryRowContext(ctx, `SELECT invitation_code,created_at FROM registration_invitations WHERE tenant_id=? AND app_id=? AND source_id=? AND agent_id=? AND invitation_code REGEXP '^[0-9]{9}$' ORDER BY created_at,invitation_code LIMIT 1`, s.Tenant, s.App, source, agent).Scan(&i.Code, &i.CreatedAt)
	return i, e
}

// StableInvitation 在账户行锁内生成稳定九位码，会员分享与注册服务共用同一凭证表。
// 随机碰撞依赖数据库唯一键检测后有限重试；不能使用用户号截断或可预测递增值。
func (r Repository) StableInvitation(ctx context.Context, tx db.Executor, s foundation.Scope, source, agent string, at time.Time) (model.Invitation, error) {
	var locked string
	if e := tx.QueryRowContext(ctx, `SELECT agent_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, agent).Scan(&locked); e != nil {
		return model.Invitation{}, e
	}
	// 命令事务使用 READ COMMITTED；等待账户锁后可读到前一请求提交的分享码，避免生成分叉。
	old, e := r.AgentInvitation(ctx, tx, s, source, agent)
	if e == nil {
		return old, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return old, e
	}
	for n := 0; n < 8; n++ {
		v, e := rand.Int(rand.Reader, big.NewInt(900000000))
		if e != nil {
			return model.Invitation{}, e
		}
		i := model.Invitation{Code: strconv.FormatInt(v.Int64()+100000000, 10), SourceID: source, AgentID: agent, CreatedAt: at}
		e = r.SaveInvitation(ctx, tx, s, i, at)
		if e == nil {
			return i, nil
		}
		var duplicate *driver.MySQLError
		if !errors.As(e, &duplicate) || duplicate.Number != 1062 {
			return model.Invitation{}, e
		}
	}
	return model.Invitation{}, errors.New("INVITATION_CODE_CAPACITY_RETRY")
}
