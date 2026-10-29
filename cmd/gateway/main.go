package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/guohai163/openai-gateway/internal/gateway"
	"github.com/guohai163/openai-gateway/web"
)

func main() {
	c, err := gateway.LoadConfig()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app, err := gateway.New(ctx, c)
	if err != nil {
		slog.Error("startup failed; verify database access, migration permissions and initial administrator configuration")
		os.Exit(1)
	}
	defer app.DB.Close()
	go app.RunJobs(ctx)
	server := &http.Server{Addr: c.Addr, Handler: app.Handler(web.Assets()), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("AI Gateway listening", "address", c.Addr)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP server failed")
		cancel()
	}
}
