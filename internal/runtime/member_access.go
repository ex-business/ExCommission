package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	identityports "github.com/ex-business/ExCommission/internal/identity/ports"
	access "github.com/ex-business/ExCommission/internal/memberaccess/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	registration "github.com/ex-business/ExCommission/internal/registration/adapters"
)

func memberSourceProvider(store *mysql.Store, scope foundation.Scope, fallback identityports.Provider) (identityports.Provider, error) {
	path := os.Getenv("AUTH_MEMBER_SOURCE_FILE")
	if path == "" {
		return fallback, nil
	}
	if store == nil {
		return nil, errors.New("会员身份来源缺少注册存储")
	}
	stat, e := os.Stat(path)
	if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return nil, errors.New("会员身份来源配置必须为私有文件")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, errors.New("会员身份来源配置不可读")
	}
	var cfg access.Config
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, errors.New("会员身份来源配置无效")
	}
	resolve := func(ctx context.Context, source, external string) (principal.Principal, error) {
		if !foundation.ValidID(external) {
			return principal.Principal{}, principal.ErrUnauthenticated
		}
		receipt, _, err := (registration.Repository{}).Receipt(ctx, store.DB, scope, source, external)
		if errors.Is(err, sql.ErrNoRows) {
			return principal.Principal{}, principal.ErrUnauthenticated
		}
		if err != nil {
			return principal.Principal{}, err
		}
		if !foundation.ValidID(receipt.UserID) || receipt.SourceID != source || receipt.ExternalUserID != external {
			return principal.Principal{}, principal.ErrUnauthenticated
		}
		// 仅注册模块已提交的回执可建立登录身份；复合编码避免不同来源/站点的同名会员碰撞。
		encoded, _ := json.Marshal([]string{scope.Tenant, scope.App, source, external})
		h := sha256.Sum256(encoded)
		p := principal.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "member-" + hex.EncodeToString(h[:]), MemberUserID: receipt.UserID}
		// 独立初始化标记只授予三项会员权限，后续登录不会恢复已撤销权限。
		err = store.Within(ctx, func(tx db.Executor) error {
			return identity.BootstrapOnce(ctx, tx, scope.Tenant, scope.App, p.Subject+":source-access-v1", p.Subject, []string{"member:read", "member:claim", "member:refer"})
		})
		return p, err
	}
	return access.New(cfg, scope.Tenant, scope.App, resolve, fallback)
}
