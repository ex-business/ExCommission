// Package domain 定义可信注册事实和首次邀请归属，不承载会员密码或计佣资格。
package domain

import "time"

type Event struct {
	SchemaVersion  string    `json:"schema_version"`
	SourceID       string    `json:"source_id"`
	EventID        string    `json:"event_id"`
	ExternalUserID string    `json:"external_user_id"`
	DisplayName    string    `json:"display_name"`
	RegisteredAt   time.Time `json:"registered_at"`
	InvitationCode *string   `json:"invitation_code"`
}

type InvitationRequest struct {
	SourceID string `json:"source_id"`
	AgentID  string `json:"agent_id"`
}

type Invitation struct {
	CreatedAt       time.Time `json:"created_at"`
	Code            string    `json:"invitation_code"`
	SourceID        string    `json:"source_id"`
	AgentID         string    `json:"agent_id"`
	RegistrationURL *string   `json:"registration_url"`
}

type Receipt struct {
	SourceID       string    `json:"source_id"`
	EventID        string    `json:"event_id"`
	ExternalUserID string    `json:"external_user_id"`
	UserID         string    `json:"user_id"`
	AgentID        string    `json:"agent_id"`
	ParentID       *string   `json:"parent_agent_id"`
	RegisteredAt   time.Time `json:"registered_at"`
}
