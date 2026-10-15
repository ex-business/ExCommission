package main

import (
	"github.com/ex-business/ExCommission/internal/runtime"
	"log/slog"
	"os"
)

func main() {
	if err := runtime.Run("api"); err != nil {
		slog.Error("服务退出", "error", err)
		os.Exit(1)
	}
}
