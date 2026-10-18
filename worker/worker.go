// Package worker 提供独立佣金执行进程入口，业务规则和资金事务仍由 ExCommission 统一维护。
package worker

import "github.com/ex-business/ExCommission/internal/runtime"

// Run 仅启动既有后台任务和健康探针，不注册 API 业务路由。
// 新旧执行进程共享原任务锁、租约和业务幂等键，不能通过拆进程绕过方案停止或独立审批。
// 环境配置、数据库迁移和 SIGTERM 退出继续沿用原实现，避免形成两套执行规则。
func Run() error {
	return runtime.Run("worker")
}
