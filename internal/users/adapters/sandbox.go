package adapters

import (
	"context"
	"errors"
	"github.com/ex-business/ExCommission/internal/users/domain"
)

// SandboxSource 仅从受控启动配置导入明确标识的本地样例，不代表外部来源联通。
type SandboxSource struct{ Records []domain.Record }

func (s SandboxSource) Fetch(_ context.Context, cursor *string) (domain.SyncPage, error) {
	if cursor != nil {
		return domain.SyncPage{}, errors.New("VALIDATION_FAILED: sandbox cursor")
	}
	return domain.SyncPage{Records: append([]domain.Record(nil), s.Records...), Watermark: "sandbox-v1"}, nil
}
