package application

import (
	"context"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	result "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"github.com/ex-business/ExCommission/internal/workflow/domain"
	"github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
	"unicode/utf8"
)

type Decision struct {
	Remark     string `json:"remark,omitempty"`
	Decision   string `json:"decision"`
	Revision   uint64 `json:"expected_revision"`
	ObjectHash string `json:"object_hash"`
	Reason     string `json:"reason"`
}
type Executor func(context.Context, db.Executor, foundation.Scope, domain.Request, time.Time) error
type Service struct {
	StepUp     func(context.Context, identity.Principal, domain.Request, string, string, any) error
	Commands   db.Commands
	Repo       ports.Repository
	Query      db.Executor
	Now        func() time.Time
	Executors  map[string]Executor
	Rejections map[string]Executor
	Failures   map[string]Executor
	Authorize  func(context.Context, db.Executor, identity.Principal, string) error
}

func (s Service) Decide(ctx context.Context, p identity.Principal, id string, input Decision, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)

	if (input.Decision != "APPROVE" && input.Decision != "REJECT") || !foundation.ValidID(input.Reason) || !utf8.ValidString(input.Remark) || utf8.RuneCountInString(input.Remark) > 1000 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	preview, err := s.Repo.Get(ctx, s.Query, scope, id, false)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	if s.Authorize != nil {
		if err = s.Authorize(ctx, s.Query, p, ApprovalPermission(preview.Type)); err != nil {
			return foundation.CommandResult{}, err
		}
	}
	if preview.Type == "SITE_AGENT_CONFIG" && input.Decision == "APPROVE" {
		if s.StepUp == nil {
			return foundation.CommandResult{}, errors.New("TOTP_REQUIRED")
		}
		if err = s.StepUp(ctx, p, preview, "APPROVE", key, input); err != nil {
			return foundation.CommandResult{}, err
		}
	}

	// 资源ID纳入命令内容，避免同键跨审批对象错误重放。
	payload := struct {
		ID       string   `json:"approval_id"`
		Decision Decision `json:"decision"`
	}{id, input}
	return s.Commands.Execute(ctx, scope, "approval-decision", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		approval, err := s.Repo.Get(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if s.Authorize != nil {
			permission := ApprovalPermission(approval.Type)
			if err = s.Authorize(ctx, tx, p, permission); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		if err = approval.CheckDecision(p.Subject, input.Revision, input.ObjectHash); err != nil {
			return foundation.CommandResult{}, err
		}
		status, execution := "REJECTED", "PENDING"
		if input.Decision == "APPROVE" {
			execute, ok := s.Executors[approval.Type]
			if !ok {
				return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
			}
			// 保存点避免领域执行失败留下一半修改；批准与FAILED状态保留，不能伪装已生效。
			if _, err = tx.ExecContext(ctx, "SAVEPOINT approval_execution"); err != nil {
				return foundation.CommandResult{}, err
			}
			err = execute(ctx, tx, scope, approval, s.Now())
			status = "APPROVED"
			execution = "SUCCEEDED"
			if err != nil {
				if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT approval_execution"); rollbackErr != nil {
					return foundation.CommandResult{}, rollbackErr
				}
				execution = "FAILED"
				if failed, ok := s.Failures[approval.Type]; ok {
					if failureErr := failed(ctx, tx, scope, approval, s.Now()); failureErr != nil {
						return foundation.CommandResult{}, failureErr
					}
				}
				if err.Error() == "STALE_APPROVAL" {
					status = "STALE"
				}
			}
		}
		if input.Decision == "REJECT" {
			if reject, ok := s.Rejections[approval.Type]; ok {
				if err = reject(ctx, tx, scope, approval, s.Now()); err != nil {
					return foundation.CommandResult{}, err
				}
			}
		}
		if err = s.Repo.Finish(ctx, tx, scope, id, p.Subject, status, execution, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		if input.Remark != "" {
			notes, ok := s.Repo.(ports.DecisionNotes)
			if !ok {
				return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
			}
			if err = notes.Note(ctx, tx, scope, id, approval.Revision+1, p.Subject, input.Remark, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		return result.Result(200, id, request, map[string]any{"approval_id": id, "approval_status": status, "execution_status": execution})
	})
}
func (s Service) Get(ctx context.Context, p identity.Principal, id string) (domain.Request, error) {
	return s.Repo.Get(ctx, s.Query, foundation.Scope{Tenant: p.Tenant, App: p.App}, id, false)
}

func ApprovalPermission(kind string) string {
	switch kind {
	case "PLAN_PUBLISH", "PLAN_ROLLBACK":
		return "plans:approve"
	case "STATEMENT":
		return "statements:approve"
	case "RECOVERY":
		return "recoveries:approve"
	case "ADJUSTMENT":
		return "adjustments:approve"
	case "RISK_RELEASE":
		return "risk:approve"
	case "AGENT_STATUS":
		return "agents:status:approve"
	case "IDENTITY_GRANT":
		return "identity:approve"
	case "MILESTONE_PROGRAM":
		return "milestones:approve"
	case "INGESTION_CONFIG":
		return "ingestion:approve"
	// 独立方案与任务告警沿用代理配置审批权，不能落入关系审批默认分支。
	case "AFFILIATE_CONFIG", "AFFILIATE_PROGRAM", "AFFILIATE_TASK_ALERT":
		return "affiliate:approve"
	case "SITE_AGENT_CONFIG":
		return "site:approve"
	case "PLATFORM_SETTING":
		return "platform:approve"
	default:
		return "relations:approve"
	}
}
