package ports

import (
	"context"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	qualification "github.com/ex-business/ExCommission/internal/qualification/domain"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"time"
)

type Repository interface {
	Rule(context.Context, db.Executor, foundation.Scope, string, bool) (model.Rule, error)
	Create(context.Context, db.Executor, foundation.Scope, model.Rule) error
	Activate(context.Context, db.Executor, foundation.Scope, string, string, string, string, string, time.Time) error
	ActiveRules(context.Context, db.Executor, foundation.Scope) ([]model.Rule, error)
	LockAgent(context.Context, db.Executor, foundation.Scope, string, string) error
	Progress(context.Context, db.Executor, foundation.Scope, string, string) (model.Progress, error)
	SaveProgress(context.Context, db.Executor, foundation.Scope, model.Progress) error
	Claim(context.Context, db.Executor, foundation.Scope, string, string, string) (model.Receipt, error)
	GetClaim(context.Context, db.Executor, foundation.Scope, string) (model.Receipt, error)
	SaveClaim(context.Context, db.Executor, foundation.Scope, model.Receipt, time.Time) error
}
type Members interface {
	DirectStatisticsAt(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) (qualification.StatisticsResult, error)
}
type Eligibility interface {
	RewardEligible(context.Context, db.Executor, foundation.Scope, string, string, time.Time) error
}
type Ledger interface {
	CreditReward(context.Context, db.Executor, foundation.Scope, money.RewardCredit, time.Time) (string, error)
}

type Inbox interface {
	Enqueue(context.Context, db.Executor, foundation.Scope, model.Event) error
	Pending(context.Context, db.Executor, foundation.Scope, time.Time) ([]model.Event, error)
	Complete(context.Context, db.Executor, foundation.Scope, string) error
	Failed(context.Context, db.Executor, foundation.Scope, model.Event, time.Time, string) error
}
