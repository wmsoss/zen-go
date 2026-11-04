// zhonger-go: zhong-er.com Go 应用层入口（M1 骨架）。
//
// 两个监听：
//   - 公共服务：/api/healthz、/shop、/lab（nginx 反代到这里，见 deploy.example/）
//   - 后台服务：/admin（只绑定 Tailscale 内网地址，公网 nginx 不代理它）
//
// 环境变量：ZHONGER_ADDR（默认 127.0.0.1:8080）、
// ZHONGER_ADMIN_ADDR（默认 127.0.0.1:8081，生产上填 Tailscale IP:8081）、
// ZHONGER_ADMIN_TOKEN（后台 Bearer 口令；未设置时后台一律 503，fail-closed）。
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zhonger/internal/admin"
	"zhonger/internal/web"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", envOr("ZHONGER_ADDR", "127.0.0.1:8080"), "public listen address")
	adminAddr := flag.String("admin-addr", envOr("ZHONGER_ADMIN_ADDR", "127.0.0.1:8081"), "admin listen address (bind to Tailscale IP in production)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	public := &http.Server{
		Addr:              *addr,
		Handler:           web.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	adm := &http.Server{
		Addr:              *adminAddr,
		Handler:           admin.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("public server listening", "addr", *addr)
		if err := public.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("public server failed", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		log.Info("admin server listening (intranet only)", "addr", *adminAddr)
		if err := adm.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("admin server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = public.Shutdown(ctx)
	_ = adm.Shutdown(ctx)
}
