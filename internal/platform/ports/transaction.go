package ports

import (
	"context"
	"database/sql"
)

// Executor 是同一事务上下文；模块只执行自身拥有表的语句，不自行提交。
type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type UnitOfWork interface {
	Within(context.Context, func(Executor) error) error
}
