// Package web: zhong-er.com 公共应用层路由（M2：商城 + 订单，支付留空）。
//
// nginx 只把 /api、/shop、/lab 三段路径反代到这里；站根 / 仍由 Hugo 静态站直出。
// 订单只走到 pending_payment：支付未接入，结账端点一律 501（见 internal/payment）。
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"zhonger/internal/payment"
	"zhonger/internal/store"
)

// Version: M2 版本标识，健康检查里返回。
const Version = "m2"

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

var shopListTmpl = template.Must(template.New("shopList").Funcs(tmplFuncs).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>商城</title></head>
<body>
<h1>商城</h1>
{{if .Products}}
<ul>
{{range .Products}}<li><a href="/shop/{{.Slug}}">{{.Name}}</a> — {{formatMoney .PriceCents .Currency}}</li>
{{end}}
</ul>
{{else}}
<p>暂无上架商品。</p>
{{end}}
<hr><small>served by zhonger-go {{.Version}}</small>
</body></html>
`))

var shopDetailTmpl = template.Must(template.New("shopDetail").Funcs(tmplFuncs).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>{{.Product.Name}} · 商城</title></head>
<body>
<h1>{{.Product.Name}}</h1>
<p>{{.Product.Description}}</p>
<p>价格：{{formatMoney .Product.PriceCents .Product.Currency}}</p>
{{if .Error}}<p style="color:red">{{.Error}}</p>{{end}}
<form method="post" action="/shop/{{.Product.Slug}}/buy">
<label>邮箱 <input type="email" name="email" required></label><br>
<label>数量 <input type="number" name="quantity" value="1" min="1" max="100" required></label><br>
<button type="submit">下单</button>
</form>
<p><a href="/shop">返回商城</a></p>
<hr><small>served by zhonger-go {{.Version}}</small>
</body></html>
`))

var orderTmpl = template.Must(template.New("order").Funcs(tmplFuncs).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>订单 #{{.Order.ID}}</title></head>
<body>
<h1>订单 #{{.Order.ID}}</h1>
<p>商品：{{.Order.ProductName}}</p>
<p>数量：{{.Order.Quantity}}</p>
<p>总价：{{formatMoney .Order.TotalCents .Order.Currency}}</p>
<p>邮箱：{{.Order.BuyerEmail}}</p>
<p>状态：{{statusText .Order.Status}}</p>
{{if eq .Order.Status "pending_payment"}}
<p>尚未接入支付，订单停留在待支付状态，不会产生扣款。支付接入后会通过邮件通知。</p>
{{end}}
{{if eq .Order.Status "cancelled"}}
<p>该订单已取消。</p>
{{end}}
<p><a href="/shop">返回商城</a></p>
<hr><small>served by zhonger-go {{.Version}}</small>
</body></html>
`))

var tmplFuncs = template.FuncMap{
	"formatMoney": formatMoney,
	"statusText":  statusText,
}

// formatMoney: 分转可读价。USD 用 $，其它币种用币种代码前缀。
func formatMoney(cents int64, currency string) string {
	if currency == "USD" {
		return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
	}
	return fmt.Sprintf("%s %d.%02d", currency, cents/100, cents%100)
}

// statusText: 订单状态中文。
func statusText(status string) string {
	switch status {
	case store.StatusPendingPayment:
		return "待支付（尚未接入支付）"
	case store.StatusCancelled:
		return "已取消"
	default:
		return status
	}
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

// handler: 持有存储与支付商的公共处理器。
type handler struct {
	store *store.Store
	pay   payment.Provider
}

func (h *handler) shopList(w http.ResponseWriter, r *http.Request) {
	products, err := h.store.ListProducts(true)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = shopListTmpl.Execute(w, map[string]any{
		"Products": products,
		"Version":  Version,
	})
}

func (h *handler) shopDetail(w http.ResponseWriter, r *http.Request) {
	p, err := h.store.GetProductBySlug(chi.URLParam(r, "slug"))
	if err != nil || !p.Active {
		http.NotFound(w, r)
		return
	}
	h.renderDetail(w, p, "")
}

func (h *handler) renderDetail(w http.ResponseWriter, p *store.Product, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = shopDetailTmpl.Execute(w, map[string]any{
		"Product": p,
		"Error":   errMsg,
		"Version": Version,
	})
}

func (h *handler) buy(w http.ResponseWriter, r *http.Request) {
	p, err := h.store.GetProductBySlug(chi.URLParam(r, "slug"))
	if err != nil || !p.Active {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		h.renderDetail(w, p, "表单无法解析")
		return
	}
	qty, err := strconv.Atoi(r.FormValue("quantity"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		h.renderDetail(w, p, "数量必须是数字")
		return
	}
	order, err := h.store.CreateOrder(p.ID, r.FormValue("email"), qty)
	if err != nil {
		if errors.Is(err, store.ErrValidation) {
			w.WriteHeader(http.StatusBadRequest)
			h.renderDetail(w, p, "下单失败：请检查邮箱与数量，商品须为上架状态")
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/orders/%s", order.Token), http.StatusSeeOther)
}

func (h *handler) orderPage(w http.ResponseWriter, r *http.Request) {
	o, err := h.store.GetOrderByToken(chi.URLParam(r, "token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = orderTmpl.Execute(w, map[string]any{
		"Order":   o,
		"Version": Version,
	})
}

func (h *handler) checkout(w http.ResponseWriter, r *http.Request) {
	o, err := h.store.GetOrderByToken(chi.URLParam(r, "token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if o.Status != store.StatusPendingPayment {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "order is " + o.Status})
		return
	}
	url, err := h.pay.CreateCheckout(r.Context(), payment.OrderRef{
		ID:          o.ID,
		ProductName: o.ProductName,
		PriceCents:  o.PriceCents,
		Currency:    o.Currency,
		BuyerEmail:  o.BuyerEmail,
		Quantity:    o.Quantity,
	})
	if err != nil {
		if errors.Is(err, payment.ErrNotConfigured) {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "payment not configured"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment provider error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"checkout_url": url})
}

func (h *handler) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	if err := h.pay.HandleWebhook(r.Context(), r); err != nil {
		if errors.Is(err, payment.ErrNotConfigured) {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "payment not configured"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment provider error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Router: 公共路由。未知路径返回 JSON 404（不暴露 Hugo 的 404 页，避免混淆）。
func Router(s *store.Store, pay payment.Provider) http.Handler {
	h := &handler{store: s, pay: pay}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)

	r.Get("/api/healthz", healthz)
	r.Get("/shop", h.shopList)
	r.Get("/shop/", h.shopList)
	r.Get("/shop/{slug}", h.shopDetail)
	r.Post("/shop/{slug}/buy", h.buy)
	r.Get("/orders/{token}", h.orderPage)
	r.Post("/api/orders/{token}/checkout", h.checkout)
	r.Post("/api/webhooks/payment", h.paymentWebhook)
	lab := placeholder("AI 玩具即将上线", "这里以后是 /lab：AI 小玩具入口（M4）。")
	r.Get("/lab", lab)
	r.Get("/lab/", lab)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	return r
}
