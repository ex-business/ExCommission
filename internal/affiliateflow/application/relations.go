package application

import (
	"context"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type depthRelations interface {
	AttributionsAtDepth(context.Context, db.Executor, foundation.Scope, []affiliate.Fact, time.Time, time.Time, int) ([]affiliate.Attribution, error)
}

// 扩展方案必须由适配器提供足够的历史层级；旧适配器只可继续处理原三层，不能静默截断。
func (s Service) attributions(ctx context.Context, tx db.Executor, sc foundation.Scope, facts []affiliate.Fact, start, end time.Time, depth int) ([]affiliate.Attribution, error) {
	if depth == 3 {
		return s.Relations.Attributions(ctx, tx, sc, facts, start, end)
	}
	if r, ok := s.Relations.(depthRelations); ok {
		return r.AttributionsAtDepth(ctx, tx, sc, facts, start, end, depth)
	}
	return nil, foundation.ErrValidation
}
