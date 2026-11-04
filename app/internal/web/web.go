// Package web: zhong-er.com 公共应用层路由（M1 骨架）。
//
// nginx 只把 /api、/shop、/lab 三段路径反代到这里；站根 / 仍由 Hugo 静态站直出。
package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Version: M1 骨架版本标识，健康检查里返回。
const Version = "m1"

var placeholderTmpl = template.Must(template.New("placeholder").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>{{.Title}}</title></head>
<body>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
<hr><small>served by zhonger-go {{.Version}} · path {{.Path}}</small>
</body></html>
`))

type placeholderData struct {
	Title   string
	Message string
	Version string
	Path    string
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "zhonger-go",
		"version": Version,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func placeholder(title, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = placeholderTmpl.Execute(w, placeholderData{
			Title:   title,
			Message: message,
			Version: Version,
			Path:    r.URL.Path,
		})
	}
}

// Router: 公共路由。未知路径返回 JSON 404（不暴露 Hugo 的 404 页，避免混淆）。
func Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)

	r.Get("/api/healthz", healthz)
	shop := placeholder("商城即将上线", "这里以后是 /shop：商品、订单与支付入口（M2）。")
	r.Get("/shop", shop)
	r.Get("/shop/", shop)
	lab := placeholder("AI 玩具即将上线", "这里以后是 /lab：AI 小玩具入口（M4）。")
	r.Get("/lab", lab)
	r.Get("/lab/", lab)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	return r
}
