// Package ports 定义会员来源与已确认注册身份的接合点。
package ports

import (
	"context"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
)

// Resolver 只能根据受信来源的外部会员号查找本系统身份，不能接受浏览器指定的内部用户号。
type Resolver func(context.Context, string, string) (principal.Principal, error)
