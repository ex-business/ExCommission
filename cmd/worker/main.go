package main

import (
	"github.com/ex-business/ExCommission/internal/runtime"
	"log/slog"
	"os"
)

// 任务仓储和租约在后续阶段装配；当前只提供可部署的独立进程。
func main() {
	if err := runtime.Run("worker"); err != nil {
		slog.Error("服务退出", "error", err)
		os.Exit(1)
	}
}
