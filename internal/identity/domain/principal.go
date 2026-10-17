package domain

import "errors"

var ErrPermission = errors.New("PERMISSION_DENIED")
var ErrUnauthenticated = errors.New("UNAUTHENTICATED")

type Principal struct {
	Tenant, App, Subject string
	MemberUserID         string
	Permissions          []string
	// 仅受信运营身份适配器设置；凭据只留服务端，不写入响应或审计。
	OpsCredential string `json:"-"`
	OpsTenant     string `json:"-"`
	OpsApp        string `json:"-"`
}

func (p Principal) Authorize(tenant, app, permission string) error {
	// 运营授权绑定内省时的原始站点，跨站调用复制并改写Principal也不能继承原站权限。
	if p.OpsCredential != "" && (p.Tenant != p.OpsTenant || p.App != p.OpsApp) {
		return ErrPermission
	}
	if p.Subject == "" || tenant == "" || app == "" || p.Tenant != tenant || p.App != app {
		return ErrPermission
	}
	for _, grant := range p.Permissions {
		if grant == permission {
			return nil
		}
	}
	return ErrPermission
}
