package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"os"
)

type Key struct {
	Ref    string `json:"key_ref"`
	Tenant string `json:"tenant_id"`
	App    string `json:"app_id"`
	Secret string `json:"secret"`
}

// KeyRing只允许读取管理员部署的私有密钥文件；页面只能选择本站引用，永不返回密钥材料。
func KeyRing(path string) ([]Key, error) {
	if path == "" {
		return []Key{}, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("INGESTION_KEYS_UNAVAILABLE")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 65536 {
		return nil, errors.New("INGESTION_KEYS_INVALID")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(raw) > 65536 {
		return nil, errors.New("INGESTION_KEYS_INVALID")
	}
	var keys []Key
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&keys) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("INGESTION_KEYS_INVALID")
	}
	seen := map[string]bool{}
	for _, k := range keys {
		sc := foundation.Scope{Tenant: k.Tenant, App: k.App}
		id := k.Tenant + "/" + k.App + "/" + k.Ref
		if !sc.Valid() || k.Ref == "" || len(k.Secret) < 32 || seen[id] {
			return nil, errors.New("INGESTION_KEYS_INVALID")
		}
		seen[id] = true
	}
	return keys, nil
}
func FindKey(path string, sc foundation.Scope, ref string) ([]byte, error) {
	keys, e := KeyRing(path)
	if e != nil {
		return nil, e
	}
	for _, k := range keys {
		if k.Tenant == sc.Tenant && k.App == sc.App && k.Ref == ref {
			return []byte(k.Secret), nil
		}
	}
	return nil, errors.New("INGESTION_KEY_NOT_FOUND")
}
