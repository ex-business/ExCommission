package domain

import "time"

// MemberProfile 只承载代理展示资料，不参与资金、身份或权限判定；未知VIP与登录时间使用null。
type MemberProfile struct {
	DisplayID string     `json:"display_id"`
	Name      string     `json:"display_name"`
	VIP       *int       `json:"vip_level"`
	Avatar    string     `json:"avatar_url"`
	LastAt    *time.Time `json:"last_login_at"`
	LastIP    string     `json:"last_login_ip"`
}
