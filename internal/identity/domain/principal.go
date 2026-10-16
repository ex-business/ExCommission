package domain

import "errors"

var ErrPermission = errors.New("PERMISSION_DENIED")
var ErrUnauthenticated = errors.New("UNAUTHENTICATED")

type Principal struct {
	Tenant, App, Subject string
	MemberUserID         string
	Permissions          []string
}

func (p Principal) Authorize(tenant, app, permission string) error {
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
