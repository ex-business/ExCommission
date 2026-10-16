// Package application 将可信注册事实原子落为用户、代理和首次关系；不修改已有审批迁移规则。
package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	"github.com/ex-business/ExCommission/internal/registration/ports"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	"log/slog"
	"net/url"
	"time"
)

type Service struct {
	Commands         db.Commands
	Repo             ports.Repository
	Users            ports.Users
	Agents           ports.Agents
	Sources          map[string]ports.Source
	RegistrationURLs map[string]string
	Now              func() time.Time
	ID               func() string
}

func (s Service) Invite(ctx context.Context, p principal.Principal, input model.InvitationRequest, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	if !foundation.ValidID(input.AgentID) || s.Sources[input.SourceID] == nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	result, err := s.Commands.Execute(ctx, scope, "registration-invite", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Agents.LockRegistration(ctx, tx, scope); err != nil {
			return foundation.CommandResult{}, err
		}
		at := s.Now().UTC().Truncate(time.Microsecond)
		if err := s.Agents.Active(ctx, tx, scope, input.AgentID, at); err != nil {
			return foundation.CommandResult{}, err
		}
		i := model.Invitation{Code: s.ID(), SourceID: input.SourceID, AgentID: input.AgentID, CreatedAt: at}
		if registrationURL := s.RegistrationURLs[input.SourceID]; registrationURL != "" {
			u, err := url.Parse(registrationURL)
			if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
				return foundation.CommandResult{}, foundation.ErrValidation
			}
			q := u.Query()
			q.Set("ic", i.Code)
			u.RawQuery = q.Encode()
			v := u.String()
			i.RegistrationURL = &v
		}
		if err := s.Repo.SaveInvitation(ctx, tx, scope, i, at); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, i.Code, request, i)
	})
	if err == nil && result.Status == 200 && !result.Replayed {
		slog.Info("邀请凭证已生成", "request_id", request, "source_id", input.SourceID, "agent_id", input.AgentID, "result", "SUCCEEDED")
	}
	return result, err
}

func (s Service) Register(ctx context.Context, p principal.Principal, raw []byte, signature, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	var selector struct {
		Source string `json:"source_id"`
	}
	if err := json.Unmarshal(raw, &selector); err != nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	source := s.Sources[selector.Source]
	if source == nil {
		return foundation.CommandResult{}, errors.New("PERMISSION_DENIED")
	}
	// 每次重放都重新验证来源，不能借旧幂等结果绕过来源签名或已撤销的来源配置。
	event, err := source.Verify(ctx, scope, raw, signature)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	if event.SourceID != selector.Source || event.SchemaVersion != "1" || !foundation.ValidID(event.EventID) || !foundation.ValidID(event.ExternalUserID) || !foundation.ValidID(event.DisplayName) || event.RegisteredAt.Year() < 1000 || event.RegisteredAt.IsZero() || event.RegisteredAt.After(s.Now()) || event.RegisteredAt.Nanosecond()%1000 != 0 || event.InvitationCode != nil && !foundation.ValidID(*event.InvitationCode) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	event.RegisteredAt = event.RegisteredAt.UTC()
	// 来源事件号和投递幂等键可变化，用户的首次注册事实不可变化；姓名变更走用户同步。
	facts := event
	facts.EventID = ""
	body, _ := json.Marshal(facts)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	created := false
	result, err := s.Commands.Execute(ctx, scope, "registration-receive", key, event, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Agents.LockRegistration(ctx, tx, scope); err != nil {
			return foundation.CommandResult{}, err
		}
		if err := s.Repo.ClaimEvent(ctx, tx, scope, event.SourceID, event.EventID, hash); err != nil {
			return foundation.CommandResult{}, err
		}
		old, oldHash, err := s.Repo.Receipt(ctx, tx, scope, event.SourceID, event.ExternalUserID)
		if err == nil {
			if hash != oldHash {
				return foundation.CommandResult{}, foundation.ErrConflict
			}
			return response.Result(200, old.AgentID, request, old)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, err
		}
		var parent *string
		if event.InvitationCode != nil {
			invite, err := s.Repo.Invitation(ctx, tx, scope, event.SourceID, *event.InvitationCode)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			if event.RegisteredAt.Before(invite.CreatedAt) {
				return foundation.CommandResult{}, foundation.ErrValidation
			}
			parent = &invite.AgentID
		}
		// 不嵌套事务：跨模块只调用端口，任一步失败由命令事务回滚，禁止出现半注册。
		uid, err := s.Users.Upsert(ctx, tx, scope, event.SourceID, user.Record{ExternalID: event.ExternalUserID, DisplayName: event.DisplayName}, event.RegisteredAt)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		aid := s.ID()
		if err = s.Agents.RegisterInitial(ctx, tx, scope, aid, uid, event.DisplayName, parent, event.RegisteredAt); err != nil {
			return foundation.CommandResult{}, err
		}
		receipt := model.Receipt{SourceID: event.SourceID, EventID: event.EventID, ExternalUserID: event.ExternalUserID, UserID: uid, AgentID: aid, ParentID: parent, RegisteredAt: event.RegisteredAt}
		if err = s.Repo.SaveReceipt(ctx, tx, scope, receipt, hash); err != nil {
			return foundation.CommandResult{}, err
		}
		created = true
		return response.Result(200, aid, request, receipt)
	})
	// 仅记录请求关联和结果；不输出邀请凭证、原始注册载荷或签名，重复成功不刷日志。
	if err == nil && result.Status == 200 && !result.Replayed && created {
		slog.Info("注册接入完成，已创建代理及首次关系", "request_id", request, "source_id", event.SourceID, "event_id", event.EventID, "agent_id", result.ResourceID, "result", "SUCCEEDED")
	}
	if err != nil {
		slog.Warn("注册接入暂未完成", "request_id", request, "source_id", event.SourceID, "event_id", event.EventID, "error", err)
	}
	return result, err
}
