package main

import (
	"context"
	"github.com/ex-business/ExCommission/internal/runtime"
	"github.com/ex-business/ex-service-router/sdk"
	"log/slog"
	"os"
)

func main() {
	if err := runtime.RunWithLifecycle("api", func(ctx context.Context) func() { return sdk.StartFromEnv(ctx, "EX_SERVICE_ROUTER_CONFIG", nil) }); err != nil {
		slog.Error("服务退出", "error", err)
		os.Exit(1)
	}
}
