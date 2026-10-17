package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

type profileCatalog struct {
	Tenant   string            `json:"tenant_id"`
	App      string            `json:"app_id"`
	Profiles []externalProfile `json:"profiles"`
}
type externalProfile struct {
	Source   string `json:"source_id"`
	External string `json:"external_user_id"`
	model.MemberProfile
}

// ReadMemberProfiles 仅读取本站受信资料，不回写会员或权限；公开会员标识与内部账务ID分离，缺失字段保持空值。
func ReadMemberProfiles(ctx context.Context, tx db.Executor, sc foundation.Scope) (map[string]model.MemberProfile, error) {
	out := map[string]model.MemberProfile{}
	external := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT user_id,source_id,external_user_id,display_name FROM users_external WHERE tenant_id=? AND app_id=?`, sc.Tenant, sc.App)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, source, ext, name string
		if err = rows.Scan(&id, &source, &ext, &name); err != nil {
			rows.Close()
			return nil, err
		}
		external[source+"\x00"+ext] = id
		out[id] = model.MemberProfile{Name: name}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	file := os.Getenv("AFFILIATE_MEMBER_CATALOG_FILE")
	if file == "" {
		return out, nil
	}
	stat, err := os.Stat(file)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() > 8<<20 {
		return nil, fmt.Errorf("代理会员资料快照不可用")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var catalog []profileCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF || len(catalog) > 1000 {
		return nil, fmt.Errorf("代理会员资料快照格式无效")
	}
	scopes := map[string]bool{}
	for _, entry := range catalog {
		key := entry.Tenant + "\x00" + entry.App
		if scopes[key] || !foundation.ValidID(entry.Tenant) || !foundation.ValidID(entry.App) {
			return nil, foundation.ErrValidation
		}
		scopes[key] = true
		seen := map[string]bool{}
		for _, p := range entry.Profiles {
			k := p.Source + "\x00" + p.External
			if seen[k] || !foundation.ValidID(p.Source) || !foundation.ValidID(p.External) || len(p.Name) > 128 || len(p.DisplayID) > 128 || len(p.Avatar) > 256 || p.VIP != nil && (*p.VIP < 0 || *p.VIP > 10000) {
				return nil, foundation.ErrValidation
			}
			seen[k] = true
			if p.Avatar != "" && (!strings.HasPrefix(p.Avatar, "/") || strings.HasPrefix(p.Avatar, "//") || strings.ContainsAny(p.Avatar, "\\\r\n")) {
				return nil, foundation.ErrValidation
			}
			if p.LastIP != "" && net.ParseIP(p.LastIP) == nil {
				return nil, foundation.ErrValidation
			}
			if p.LastAt != nil && (p.LastAt.Year() < 2000 || p.LastAt.After(time.Now().Add(time.Minute))) {
				return nil, foundation.ErrValidation
			}
			if entry.Tenant == sc.Tenant && entry.App == sc.App {
				if id, ok := external[k]; ok {
					if p.Name == "" {
						p.Name = out[id].Name
					}
					out[id] = p.MemberProfile
				}
			}
		}
	}
	return out, nil
}
