// Package runtime 提供进程启动和关闭，不包含业务规则。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
)

func Handler(service string) http.Handler { return handler(service, nil) }
func handler(service string, ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"service": service, "status": "up"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready == nil {
			http.Error(w, "not initialized", http.StatusServiceUnavailable)
			return
		}
		if err := ready(r.Context()); err != nil {
			http.Error(w, "dependency unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	return mux
}

func Run(service string) error {
	initCtx, cancelInit := context.WithTimeout(context.Background(), 45*time.Second)
	store, err := mysql.Open(initCtx, os.Getenv("DB_ADDRESS"), os.Getenv("DB_USER"), os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		cancelInit()
		return err
	}
	defer store.DB.Close()
	err = store.Migrate(initCtx)
	cancelInit()
	if err != nil {
		return err
	}

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	health := handler(service, store.DB.PingContext)
	router := http.NewServeMux()
	router.Handle("/healthz", health)
	router.Handle("/readyz", health)
	if service == "api" {
		business, err := businessHandler(context.Background(), store)
		if err != nil {
			return err
		}
		router.Handle("/v1/", business)
	}
	server := &http.Server{Addr: address, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if service == "worker" {
		worker := buildWorker(store)
		payments := paymentService(store)
		refunds := refundService(store)
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					jobCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
					err := payments.Recover(jobCtx, localScope())
					if err == nil {
						err = refunds.Recover(jobCtx, localScope())
					}
					cancel()
					if err != nil && ctx.Err() == nil {
						slog.Error("付款恢复失败，保留原资金号与占用", "service", service)
					}
				}
			}
		}()

		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					jobCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
					_, err := worker.RunOnce(jobCtx, localScope())
					cancel()
					if err != nil && ctx.Err() == nil {
						slog.Error("任务处理失败，保留原任务等待恢复", "service", service)
					}
				}
			}
		}()
	}
	failure := make(chan error, 1)
	go func() { failure <- server.ListenAndServe() }()
	slog.Info("服务启动", "service", service, "address", address)
	select {
	case err := <-failure:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
