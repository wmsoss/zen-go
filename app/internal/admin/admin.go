// Package admin: 后台路由（M1 骨架）。
//
// 安全模型（按架构文档“后台只走 Tailscale 内网 + 单管理员账号”）：
//  1. 网络层：本服务只绑定 Tailscale 内网地址，公网 nginx 不代理 /admin；
//  2. 口令层：ZHONGER_ADMIN_TOKEN 未设置时一律 503（fail-closed），
//     设置后须带 Authorization: Bearer <token>，否则 401。
// 完整管理员账号与运维面板属于 M4，本骨架只验证“内网可达 + 口令门”。
package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const Version = "m1"

var startedAt = time.Now()

// tokenAuth: fail-closed 的 Bearer 口令中间件。
func tokenAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("ZHONGER_ADMIN_TOKEN")
		if token == "" {
			http.Error(w, "admin not configured", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func statusPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>运维后台（M1 骨架）</title></head>
<body><h1>运维后台</h1>
<p>内网可达验证通过。完整运维面板（订单、库存、流量）属于 M4。</p>
</body></html>
`))
}

func statusAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"service":        "zhonger-go-admin",
		"version":        Version,
		"started_at":     startedAt.UTC().Format(time.RFC3339),
		"uptime_seconds": int(time.Since(startedAt).Seconds()),
	})
}

// Router: 后台路由（必须只挂在内网监听地址上）。
func Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	r.Use(tokenAuth)

	r.Get("/admin/", statusPage)
	r.Get("/admin", statusPage)
	r.Get("/admin/api/status", statusAPI)
	return r
}
