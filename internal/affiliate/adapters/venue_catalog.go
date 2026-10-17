package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"net/url"
	"os"
)

type VenueCatalogEntry struct {
	Tenant string        `json:"tenant_id"`
	App    string        `json:"app_id"`
	Venues []model.Venue `json:"venues"`
}

// 配置外部业务场馆目录的完整快照，按站点隔离；未配置的站点继续读取原数据库目录。
// 目录只允许受信服务端文件，拒绝不完整或重复条目，不把浏览器输入当成上游场馆事实。
func externalVenues(scope foundation.Scope) ([]model.Venue, bool, error) {
	file := os.Getenv("AFFILIATE_VENUE_CATALOG_FILE")
	if file == "" {
		return nil, false, nil
	}
	stat, e := os.Stat(file)
	if e != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 || stat.Mode().Perm()&0022 != 0 {
		return nil, false, fmt.Errorf("外部场馆目录文件不可用")
	}
	raw, e := os.ReadFile(file)
	if e != nil {
		return nil, false, e
	}
	var entries []VenueCatalogEntry
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&entries) != nil || d.Decode(new(any)) != io.EOF || len(entries) > 1000 {
		return nil, false, fmt.Errorf("外部场馆目录格式无效")
	}
	seenScopes := map[string]bool{}
	var selected []model.Venue
	for _, entry := range entries {
		key := entry.Tenant + "\x00" + entry.App
		if !foundation.ValidID(entry.Tenant) || !foundation.ValidID(entry.App) || seenScopes[key] || len(entry.Venues) == 0 || len(entry.Venues) > 100 {
			return nil, false, fmt.Errorf("外部场馆目录作用域无效")
		}
		seenScopes[key] = true
		seen := map[string]bool{}
		for _, v := range entry.Venues {
			u, err := url.Parse(v.IconURL)
			local := len(v.IconURL) > 0 && v.IconURL[0] == '/' && !bytes.HasPrefix([]byte(v.IconURL), []byte("//"))
			if !foundation.ValidID(v.ID) || v.Name == "" || len(v.Name) > 128 || seen[v.ID] || err != nil || u.User != nil || (!local && (u.Scheme != "https" || u.Host == "")) {
				return nil, false, fmt.Errorf("外部场馆类型或LOGO无效")
			}
			seen[v.ID] = true
		}
		if entry.Tenant == scope.Tenant && entry.App == scope.App {
			selected = entry.Venues
		}
	}
	return selected, selected != nil, nil
}
