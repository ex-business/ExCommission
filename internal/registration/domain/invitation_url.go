package domain

import (
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net"
	"net/url"
)

// InvitationURL 指向真实注册入口，统一 ic 参数；HTTP 仅用于回环沙箱。
func InvitationURL(base, code string) (string, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", foundation.ErrValidation
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || ip == nil || !ip.IsLoopback()) {
		return "", foundation.ErrValidation
	}
	q := u.Query()
	q.Set("ic", code)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
