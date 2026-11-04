// Package admin: 后台路由（M2：商品与订单管理）。
//
// 安全模型（按架构文档“后台只走 Tailscale 内网 + 单管理员账号”）：
//  1. 网络层：本服务只绑定 Tailscale 内网地址，公网 nginx 不代理 /admin；
//  2. 口令层：ZHONGER_ADMIN_TOKEN 未设置时一律 503（fail-closed），
//     设置后须带 Authorization: Bearer <token>，否则 401。
//
// 页面为服务端渲染 HTML， destructive 操作（上下架、取消订单）用普通
// form POST，另带 htmx 属性做渐进增强：htm 能加载时局部刷新，
// 加载不到时退化为整页跳转，两种路径都经过同一处理函数。
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"zhonger/internal/store"
)

const Version = "m2"

var startedAt = time.Now()

var adminTmpl = template.Must(template.New("admin").Funcs(template.FuncMap{
	"formatMoney": formatMoney,
}).Parse(`
{{define "dashboard"}}<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>运维后台</title>
<script src="https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js"></script></head>
<body><h1>运维后台</h1>
<p>内网可达验证通过。商品 {{.ProductCount}} 个，订单 {{.OrderCount}} 个。</p>
<ul>
<li><a href="/admin/products">商品管理</a></li>
<li><a href="/admin/orders">订单管理</a></li>
<li><a href="/admin/api/status">状态接口（JSON）</a></li>
</ul>
</body></html>
{{end}}

{{define "productRow"}}<tr>
<td>{{.ID}}</td><td>{{.Slug}}</td><td>{{.Name}}</td>
<td>{{formatMoney .PriceCents .Currency}}</td><td>{{if .Active}}上架{{else}}下架{{end}}</td>
<td><a href="/admin/products/{{.ID}}/edit">编辑</a></td>
<td><form method="post" action="/admin/products/{{.ID}}/toggle" hx-post="/admin/products/{{.ID}}/toggle" hx-target="closest tr" hx-swap="outerHTML"><button type="submit">{{if .Active}}下架{{else}}上架{{end}}</button></form></td>
</tr>{{end}}

{{define "productList"}}<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>商品管理</title>
<script src="https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js"></script></head>
<body><h1>商品管理</h1>
<p><a href="/admin/products/new">新建商品</a> · <a href="/admin/">返回后台</a></p>
{{if .Products}}
<table border="1"><tr><th>ID</th><th>Slug</th><th>名称</th><th>价格</th><th>状态</th><th></th><th></th></tr>
{{range .Products}}{{template "productRow" .}}{{end}}
</table>
{{else}}
<p>暂无商品。</p>
{{end}}
</body></html>
{{end}}

{{define "productForm"}}<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>{{.Title}}</title></head>
<body><h1>{{.Title}}</h1>
{{if .Error}}<p style="color:red">{{.Error}}</p>{{end}}
<form method="post" action="{{.Action}}">
<label>Slug <input type="text" name="slug" value="{{.Product.Slug}}" required></label><br>
<label>名称 <input type="text" name="name" value="{{.Product.Name}}" required></label><br>
<label>描述 <textarea name="description">{{.Product.Description}}</textarea></label><br>
<label>价格 <input type="text" name="price" value="{{.Price}}" required></label>（如 19.99，单位为币种主单位）<br>
<label>币种 <input type="text" name="currency" value="{{.Product.Currency}}" maxlength="3" required></label><br>
<label>上架 <input type="checkbox" name="active" {{if .Product.Active}}checked{{end}}></label><br>
<button type="submit">保存</button>
</form>
<p><a href="/admin/products">返回商品列表</a></p>
</body></html>
{{end}}

{{define "orderList"}}<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>订单管理</title></head>
<body><h1>订单管理</h1>
<p><a href="/admin/">返回后台</a></p>
{{if .Orders}}
<table border="1"><tr><th>ID</th><th>商品</th><th>邮箱</th><th>数量</th><th>总价</th><th>状态</th><th></th></tr>
{{range .Orders}}<tr>
<td>{{.ID}}</td><td>{{.ProductName}}</td><td>{{.BuyerEmail}}</td><td>{{.Quantity}}</td>
<td>{{formatMoney .TotalCents .Currency}}</td><td>{{.Status}}</td>
<td><a href="/admin/orders/{{.ID}}">查看</a></td>
</tr>{{end}}
</table>
{{else}}
<p>暂无订单。</p>
{{end}}
</body></html>
{{end}}

{{define "orderStatus"}}<div id="order-status"><p>状态：{{.Status}}</p>
{{if eq .Status "pending_payment"}}<p>待支付（尚未接入支付）。</p>{{end}}
{{if eq .Status "cancelled"}}<p>该订单已取消。</p>{{end}}</div>{{end}}

{{define "orderDetail"}}<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>订单 #{{.Order.ID}}</title>
<script src="https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js"></script></head>
<body><h1>订单 #{{.Order.ID}}</h1>
<p>商品：{{.Order.ProductName}}（ID {{.Order.ProductID}}）</p>
<p>数量：{{.Order.Quantity}}</p>
<p>总价：{{formatMoney .Order.TotalCents .Order.Currency}}</p>
<p>邮箱：{{.Order.BuyerEmail}}</p>
{{if .Error}}<p style="color:red">{{.Error}}</p>{{end}}
{{template "orderStatus" .Order}}
{{if eq .Order.Status "pending_payment"}}
<form method="post" action="/admin/orders/{{.Order.ID}}/cancel" hx-post="/admin/orders/{{.Order.ID}}/cancel" hx-target="#order-status" hx-swap="outerHTML"><button type="submit">取消订单</button></form>
{{end}}
<p><a href="/admin/orders">返回订单列表</a></p>
</body></html>
{{end}}
`))

// formatMoney: 分转可读价（与公共端一致）。
func formatMoney(cents int64, currency string) string {
	if currency == "USD" {
		return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
	}
	return fmt.Sprintf("%s %d.%02d", currency, cents/100, cents%100)
}

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

// parsePriceCents: "19.99" 式十进制价格转分。只接受非负数、最多两位小数。
func parsePriceCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty price")
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if intPart == "" {
		return 0, fmt.Errorf("invalid price %q", s)
	}
	for _, c := range intPart {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid price %q", s)
		}
	}
	if len(fracPart) > 2 {
		return 0, fmt.Errorf("invalid price %q", s)
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid price %q", s)
		}
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	dollars, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid price %q", s)
	}
	cents, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid price %q", s)
	}
	return dollars*100 + cents, nil
}

// handler: 持有存储的后台处理器。
type handler struct {
	store *store.Store
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	products, err := h.store.ListProducts(false)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	orders, err := h.store.ListOrders()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.ExecuteTemplate(w, "dashboard", map[string]any{
		"ProductCount": len(products),
		"OrderCount":   len(orders),
	})
}

func (h *handler) statusAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"service":        "zhonger-go-admin",
		"version":        Version,
		"started_at":     startedAt.UTC().Format(time.RFC3339),
		"uptime_seconds": int(time.Since(startedAt).Seconds()),
	})
}

func (h *handler) productList(w http.ResponseWriter, r *http.Request) {
	products, err := h.store.ListProducts(false)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.ExecuteTemplate(w, "productList", map[string]any{"Products": products})
}

type productFormData struct {
	Title    string
	Action   string
	Product  *store.Product
	Price    string
	Error    string
}

func (h *handler) renderProductForm(w http.ResponseWriter, status int, data productFormData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = adminTmpl.ExecuteTemplate(w, "productForm", data)
}

func (h *handler) productNewForm(w http.ResponseWriter, r *http.Request) {
	h.renderProductForm(w, http.StatusOK, productFormData{
		Title:   "新建商品",
		Action:  "/admin/products",
		Product: &store.Product{Currency: "USD", Active: true},
	})
}

// productFromForm: 从表单解析商品字段（不含 ID）。价格格式错误时
// 仍返回已填字段，供表单回显。
func productFromForm(r *http.Request) (*store.Product, string, error) {
	p := &store.Product{Currency: "USD", Active: true}
	if err := r.ParseForm(); err != nil {
		return p, "", fmt.Errorf("表单无法解析")
	}
	p.Slug = strings.TrimSpace(r.FormValue("slug"))
	p.Name = r.FormValue("name")
	p.Description = r.FormValue("description")
	p.Currency = strings.ToUpper(strings.TrimSpace(r.FormValue("currency")))
	p.Active = r.FormValue("active") == "on"
	priceStr := r.FormValue("price")
	price, err := parsePriceCents(priceStr)
	if err != nil {
		return p, priceStr, fmt.Errorf("价格格式错误：请输入如 19.99 的数字")
	}
	p.PriceCents = price
	return p, priceStr, nil
}

func (h *handler) productCreate(w http.ResponseWriter, r *http.Request) {
	p, priceStr, err := productFromForm(r)
	if err != nil {
		h.renderProductForm(w, http.StatusBadRequest, productFormData{
			Title: "新建商品", Action: "/admin/products", Product: p, Price: priceStr, Error: err.Error(),
		})
		return
	}
	if p == nil {
		p = &store.Product{Currency: "USD", Active: true}
	}
	if err := h.store.CreateProduct(p); err != nil {
		msg := "保存失败"
		if errors.Is(err, store.ErrValidation) || errors.Is(err, store.ErrConflict) {
			msg = "保存失败：请检查 slug（唯一、小写字母数字连字符）、名称、价格与币种"
		}
		h.renderProductForm(w, http.StatusBadRequest, productFormData{
			Title: "新建商品", Action: "/admin/products", Product: p, Price: priceStr, Error: msg,
		})
		return
	}
	http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
}

func (h *handler) productEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := h.store.GetProduct(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.renderProductForm(w, http.StatusOK, productFormData{
		Title:  "编辑商品",
		Action: fmt.Sprintf("/admin/products/%d/edit", p.ID),
		Product: p,
		Price:  fmt.Sprintf("%d.%02d", p.PriceCents/100, p.PriceCents%100),
	})
}

func (h *handler) productUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, priceStr, err := productFromForm(r)
	action := fmt.Sprintf("/admin/products/%d/edit", id)
	if err != nil {
		if p == nil {
			p = &store.Product{Currency: "USD"}
		}
		p.ID = id
		h.renderProductForm(w, http.StatusBadRequest, productFormData{
			Title: "编辑商品", Action: action, Product: p, Price: priceStr, Error: err.Error(),
		})
		return
	}
	p.ID = id
	if err := h.store.UpdateProduct(p); err != nil {
		msg := "保存失败"
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, store.ErrValidation) || errors.Is(err, store.ErrConflict) {
			msg = "保存失败：请检查 slug（唯一、小写字母数字连字符）、名称、价格与币种"
		}
		h.renderProductForm(w, http.StatusBadRequest, productFormData{
			Title: "编辑商品", Action: action, Product: p, Price: priceStr, Error: msg,
		})
		return
	}
	http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
}

func (h *handler) productToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := h.store.GetProduct(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.store.SetProductActive(id, !p.Active); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Active = !p.Active
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = adminTmpl.ExecuteTemplate(w, "productRow", p)
		return
	}
	http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
}

func (h *handler) orderList(w http.ResponseWriter, r *http.Request) {
	orders, err := h.store.ListOrders()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.ExecuteTemplate(w, "orderList", map[string]any{"Orders": orders})
}

func (h *handler) orderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	o, err := h.store.GetOrder(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.ExecuteTemplate(w, "orderDetail", map[string]any{"Order": o})
}

func (h *handler) orderCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := h.store.CancelOrder(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		// 已取消等状态：回到详情页并提示。
		o, gerr := h.store.GetOrder(id)
		if gerr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = adminTmpl.ExecuteTemplate(w, "orderDetail", map[string]any{
			"Order": o,
			"Error": "该订单当前状态不可取消",
		})
		return
	}
	o, err := h.store.GetOrder(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = adminTmpl.ExecuteTemplate(w, "orderStatus", o)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/orders/%d", id), http.StatusSeeOther)
}

// Router: 后台路由（必须只挂在内网监听地址上）。
func Router(s *store.Store) http.Handler {
	h := &handler{store: s}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	r.Use(tokenAuth)

	r.Get("/admin/", h.dashboard)
	r.Get("/admin", h.dashboard)
	r.Get("/admin/api/status", h.statusAPI)
	r.Get("/admin/products", h.productList)
	r.Get("/admin/products/new", h.productNewForm)
	r.Post("/admin/products", h.productCreate)
	r.Get("/admin/products/{id}/edit", h.productEditForm)
	r.Post("/admin/products/{id}/edit", h.productUpdate)
	r.Post("/admin/products/{id}/toggle", h.productToggle)
	r.Get("/admin/orders", h.orderList)
	r.Get("/admin/orders/{id}", h.orderDetail)
	r.Post("/admin/orders/{id}/cancel", h.orderCancel)
	return r
}
