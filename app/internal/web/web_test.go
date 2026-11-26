package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"zhonger/internal/payment"
	"zhonger/internal/store"
)

func newTestEnv(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, p := range []*store.Product{
		{Slug: "book", Name: "测试商品", Description: "一本好书", PriceCents: 1999, Currency: "USD", Active: true},
		{Slug: "off", Name: "下架商品", PriceCents: 100, Currency: "USD", Active: false},
	} {
		if err := s.CreateProduct(p); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	srv := httptest.NewServer(Router(s, payment.NoopProvider{}))
	t.Cleanup(srv.Close)
	return srv, s
}

var noRedirectClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHealthzAndLab(t *testing.T) {
	srv, _ := newTestEnv(t)
	code, body := getBody(t, srv.URL+"/api/healthz")
	if code != 200 || !strings.Contains(body, `"version":"m2"`) {
		t.Fatalf("healthz: %d %s", code, body)
	}
	code, _ = getBody(t, srv.URL+"/lab")
	if code != 200 {
		t.Fatalf("lab: %d", code)
	}
	code, _ = getBody(t, srv.URL+"/nope")
	if code != 404 {
		t.Fatalf("unknown path: %d", code)
	}
}

func TestShopPages(t *testing.T) {
	srv, _ := newTestEnv(t)
	code, body := getBody(t, srv.URL+"/shop")
	if code != 200 || !strings.Contains(body, "测试商品") || strings.Contains(body, "下架商品") {
		t.Fatalf("shop list: %d %s", code, body)
	}
	code, body = getBody(t, srv.URL+"/shop/book")
	if code != 200 || !strings.Contains(body, "$19.99") {
		t.Fatalf("shop detail: %d %s", code, body)
	}
	code, _ = getBody(t, srv.URL+"/shop/off")
	if code != 404 {
		t.Fatalf("inactive detail: %d, want 404", code)
	}
	code, _ = getBody(t, srv.URL+"/shop/missing")
	if code != 404 {
		t.Fatalf("missing detail: %d, want 404", code)
	}
}

func TestBuyFlow(t *testing.T) {
	srv, s := newTestEnv(t)

	// 正常下单：303 到订单页，订单为 pending_payment。
	resp, err := noRedirectClient.PostForm(srv.URL+"/shop/book/buy", url.Values{
		"email":    {"buyer@example.com"},
		"quantity": {"2"},
	})
	if err != nil {
		t.Fatalf("buy POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("buy: status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/orders/") {
		t.Fatalf("buy: Location = %q", loc)
	}
	code, body := getBody(t, srv.URL+loc)
	if code != 200 || !strings.Contains(body, "尚未接入支付") || !strings.Contains(body, "$39.98") || !strings.Contains(body, "buyer@example.com") {
		t.Fatalf("order page: %d %s", code, body)
	}
	orders, _ := s.ListOrders()
	if len(orders) != 1 || orders[0].Status != store.StatusPendingPayment {
		t.Fatalf("stored order: %+v", orders)
	}
	orderToken := orders[0].Token

	// 非法输入：坏邮箱、坏数量、下架商品。
	for _, tc := range []struct {
		slug   string
		email  string
		qty    string
	}{
		{"book", "not-an-email", "1"},
		{"book", "buyer@example.com", "0"},
		{"book", "buyer@example.com", "abc"},
		{"off", "buyer@example.com", "1"},
	} {
		resp, err := noRedirectClient.PostForm(srv.URL+"/shop/"+tc.slug+"/buy", url.Values{
			"email":    {tc.email},
			"quantity": {tc.qty},
		})
		if err != nil {
			t.Fatalf("buy POST: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Fatalf("buy(%v): status = %d, want 400/404", tc, resp.StatusCode)
		}
	}
	if orders, _ := s.ListOrders(); len(orders) != 1 {
		t.Fatalf("invalid buys created orders: %d", len(orders))
	}

	// 结账：501 支付未接入，凭 token 访问。
	resp, err = noRedirectClient.Post(srv.URL+fmt.Sprintf("/api/orders/%s/checkout", orderToken), "application/json", nil)
	if err != nil {
		t.Fatalf("checkout POST: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented || !strings.Contains(string(b), "payment not configured") {
		t.Fatalf("checkout: %d %s", resp.StatusCode, b)
	}

	// 支付回调入口：同样 501。
	resp, err = noRedirectClient.Post(srv.URL+"/api/webhooks/payment", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("webhook POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("webhook: %d, want 501", resp.StatusCode)
	}

	// 不存在的订单页与结账；旧的 id 形式 URL 也已失效（O1 修复）。
	code, _ = getBody(t, srv.URL+"/orders/99999")
	if code != 404 {
		t.Fatalf("missing order page: %d", code)
	}
	code, _ = getBody(t, srv.URL+"/orders/1")
	if code != 404 {
		t.Fatalf("id-based order page: %d, want 404", code)
	}
	resp, err = noRedirectClient.Post(srv.URL+"/api/orders/1/checkout", "application/json", nil)
	if err != nil {
		t.Fatalf("checkout POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("id-based checkout: %d, want 404", resp.StatusCode)
	}
}
