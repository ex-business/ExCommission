package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/users/domain"
)

// Source 是编译时装配的可信来源，映射版本不可被同名替换。
type Source interface {
	Fetch(context.Context, *string) (domain.SyncPage, error)
}
