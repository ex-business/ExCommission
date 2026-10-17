package adapters

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	afdomain "github.com/ex-business/ExCommission/internal/affiliate/domain"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"io"
	"os"
	"sort"
	"time"
)

type SiteTarget struct {
	Tenant string `json:"tenant_id"`
	App    string `json:"app_id"`
}
type SiteAccess struct {
	Tenant          string       `json:"owner_tenant_id"`
	App             string       `json:"owner_app_id"`
	Subject         string       `json:"subject_id"`
	Targets         []SiteTarget `json:"targets"`
	WritableTargets []SiteTarget `json:"writable_targets,omitempty"`
}

// SiteCatalog 将跨站只读授权作为独立端口配置，不能因local-operator在多站同名就推断有全站权限。
// 原身份提供方、站点识别与写接口仍绑定原Scope，客户端传目标站点不能扩大授权。
type SiteCatalog struct{ AccessFile string }

func (c SiteCatalog) Targets(p principal.Principal) ([]SiteTarget, error) {
	own := SiteTarget{Tenant: p.Tenant, App: p.App}
	targets := map[string]SiteTarget{p.Tenant + "\x00" + p.App: own}
	if c.AccessFile != "" {
		stat, e := os.Stat(c.AccessFile)
		if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
			return nil, errors.New("跨站只读授权文件不可用")
		}
		raw, e := os.ReadFile(c.AccessFile)
		if e != nil || len(raw) > 65536 {
			return nil, errors.New("跨站只读授权文件无效")
		}
		var entries []SiteAccess
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&entries) != nil || d.Decode(new(any)) != io.EOF {
			return nil, errors.New("跨站只读授权配置无效")
		}
		for _, entry := range entries {
			if !foundation.ValidID(entry.Tenant) || !foundation.ValidID(entry.App) || !foundation.ValidID(entry.Subject) || len(entry.Targets)+len(entry.WritableTargets) > 1000 {
				return nil, errors.New("跨站只读授权范围无效")
			}
			if entry.Tenant != p.Tenant || entry.App != p.App || entry.Subject != p.Subject {
				continue
			}
			for _, target := range append(entry.Targets, entry.WritableTargets...) {
				if !foundation.ValidID(target.Tenant) || !foundation.ValidID(target.App) {
					return nil, errors.New("跨站只读授权范围无效")
				}
				targets[target.Tenant+"\x00"+target.App] = target
			}
		}
	}
	out := []SiteTarget{}
	for _, target := range targets {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant == out[j].Tenant {
			return out[i].App < out[j].App
		}
		return out[i].Tenant < out[j].Tenant
	})
	return out, nil
}

// Writable 只表达明确授权，调用方还须校验目标站点业务权限；旧targets永远不自动升级为写授权。
func (c SiteCatalog) Writable(p principal.Principal, target SiteTarget) (bool, error) {
	if _, e := c.Targets(p); e != nil {
		return false, e
	}
	if target.Tenant == p.Tenant && target.App == p.App {
		return true, nil
	}
	if c.AccessFile == "" {
		return false, nil
	}
	raw, e := os.ReadFile(c.AccessFile)
	if e != nil {
		return false, e
	}
	var entries []SiteAccess
	if json.Unmarshal(raw, &entries) != nil {
		return false, errors.New("跨站授权配置无效")
	}
	for _, entry := range entries {
		if entry.Tenant == p.Tenant && entry.App == p.App && entry.Subject == p.Subject {
			for _, t := range entry.WritableTargets {
				if t == target {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

type SiteConfiguration struct {
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
	Operator  string     `json:"operator"`
	Writable  bool       `json:"writable"`

	SiteTarget
	State afdomain.Configuration `json:"configuration"`
}

func (c SiteCatalog) Read(ctx context.Context, tx db.Executor, p principal.Principal, at time.Time) ([]SiteConfiguration, error) {
	targets, e := c.Targets(p)
	if e != nil {
		return nil, e
	}
	out := []SiteConfiguration{}
	for _, target := range targets {
		state, e := (affiliate.Repository{}).Configuration(ctx, tx, foundation.Scope{Tenant: target.Tenant, App: target.App}, at, false)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		row := SiteConfiguration{SiteTarget: target, State: state}
		sc := foundation.Scope{Tenant: target.Tenant, App: target.App}
		var created, updated sql.NullTime
		var operator sql.NullString
		if state.Configured || state.Scheduled != nil {
			e = tx.QueryRowContext(ctx, `SELECT MIN(w.created_at) FROM affiliate_config_versions v JOIN workflow_requests w ON w.tenant_id=v.tenant_id AND w.app_id=v.app_id AND w.approval_id=v.approval_id WHERE v.tenant_id=? AND v.app_id=?`, sc.Tenant, sc.App).Scan(&created)
			if e != nil {
				return nil, e
			}
			e = tx.QueryRowContext(ctx, `SELECT w.created_at,w.applicant_id FROM affiliate_config_versions v JOIN workflow_requests w ON w.tenant_id=v.tenant_id AND w.app_id=v.app_id AND w.approval_id=v.approval_id WHERE v.tenant_id=? AND v.app_id=? ORDER BY v.revision DESC LIMIT 1`, sc.Tenant, sc.App).Scan(&updated, &operator)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
		}
		if created.Valid {
			row.CreatedAt = &created.Time
		}
		if updated.Valid {
			row.UpdatedAt = &updated.Time
		}
		row.Operator = operator.String
		out = append(out, row)
	}
	return out, nil
}
