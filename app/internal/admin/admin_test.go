package admin

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"zhonger/internal/store"
)

func newTestEnv(t *testing.T, token string) (*httptest.Server, *store.Store) {
	t.Helper()
	t.Setenv("ZHONGER_ADMIN_TOKEN", token)
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateProduct(&store.Product{Slug: "book", Name: "测试商品", PriceCents: 1999, Currency: "USD", Active: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := httptest.NewServer(Router(s))
	t.Cleanup(srv.Close)
	return srv, s
}

var noRedirectClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

func doReq(t *testing.T, method, url string, form url.Values, token string, hx bool) (int, http.Header, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func TestAuthFailClosed(t *testing.T) {
	// 未配口令：一律 503。
	srv, _ := newTestEnv(t, "")
	code, _, _ := doReq(t, "GET", srv.URL+"/admin/", nil, "", false)
	if code != 503 {
		t.Fatalf("no token configured: %d, want 503", code)
	}

	// 配了口令：无口令 401，错口令 401，对口令 200。
	srv, _ = newTestEnv(t, "test-token")
	code, _, _ = doReq(t, "GET", srv.URL+"/admin/", nil, "", false)
	if code != 401 {
		t.Fatalf("no bearer: %d, want 401", code)
	}
	code, _, _ = doReq(t, "GET", srv.URL+"/admin/", nil, "wrong", false)
	if code != 401 {
		t.Fatalf("wrong bearer: %d, want 401", code)
	}
	code, _, body := doReq(t, "GET", srv.URL+"/admin/", nil, "test-token", false)
	if code != 200 || !strings.Contains(body, "商品管理") {
		t.Fatalf("authed dashboard: %d %s", code, body)
	}
	code, _, body = doReq(t, "GET", srv.URL+"/admin/api/status", nil, "test-token", false)
	if code != 200 || !strings.Contains(body, `"version":"m2"`) {
		t.Fatalf("status api: %d %s", code, body)
	}
}

func TestProductCRUD(t *testing.T) {
	srv, s := newTestEnv(t, "test-token")
	const tok = "test-token"

	code, _, body := doReq(t, "GET", srv.URL+"/admin/products", nil, tok, false)
	if code != 200 || !strings.Contains(body, "测试商品") {
		t.Fatalf("product list: %d %s", code, body)
	}

	// 新建：成功 303，坏 slug 400。
	code, _, _ = doReq(t, "POST", srv.URL+"/admin/products", url.Values{
		"slug": {"pen"}, "name": {"钢笔"}, "description": {""},
		"price": {"9.50"}, "currency": {"USD"}, "active": {"on"},
	}, tok, false)
	if code != 303 {
		t.Fatalf("create: %d, want 303", code)
	}
	if p, err := s.GetProductBySlug("pen"); err != nil || p.PriceCents != 950 {
		t.Fatalf("created product: %+v, %v", p, err)
	}
	code, _, _ = doReq(t, "POST", srv.URL+"/admin/products", url.Values{
		"slug": {"Bad Slug"}, "name": {"x"}, "price": {"1"}, "currency": {"USD"},
	}, tok, false)
	if code != 400 {
		t.Fatalf("bad slug create: %d, want 400", code)
	}

	// 编辑 + 上下架切换（含 HTMX 片段）。
	p, _ := s.GetProductBySlug("pen")
	code, _, _ = doReq(t, "POST", srv.URL+fmt.Sprintf("/admin/products/%d/edit", p.ID), url.Values{
		"slug": {"pen"}, "name": {"钢笔 Pro"}, "description": {""},
		"price": {"12.00"}, "currency": {"USD"}, "active": {"on"},
	}, tok, false)
	if code != 303 {
		t.Fatalf("update: %d, want 303", code)
	}
	if p, _ := s.GetProductBySlug("pen"); p.Name != "钢笔 Pro" || p.PriceCents != 1200 {
		t.Fatalf("updated product: %+v", p)
	}
	code, _, body = doReq(t, "POST", srv.URL+fmt.Sprintf("/admin/products/%d/toggle", p.ID), nil, tok, true)
	if code != 200 || !strings.Contains(body, "<tr>") || !strings.Contains(body, "下架") {
		t.Fatalf("toggle HX: %d %s", code, body)
	}
	if p, _ := s.GetProductBySlug("pen"); p.Active {
		t.Fatalf("toggle did not deactivate")
	}
}

func TestOrderCancel(t *testing.T) {
	srv, s := newTestEnv(t, "test-token")
	const tok = "test-token"
	p, _ := s.GetProductBySlug("book")
	o, err := s.CreateOrder(p.ID, "buyer@example.com", 1)
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}

	code, _, body := doReq(t, "GET", srv.URL+"/admin/orders", nil, tok, false)
	if code != 200 || !strings.Contains(body, "buyer@example.com") {
		t.Fatalf("order list: %d %s", code, body)
	}
	code, _, body = doReq(t, "GET", srv.URL+fmt.Sprintf("/admin/orders/%d", o.ID), nil, tok, false)
	if code != 200 || !strings.Contains(body, "待支付") {
		t.Fatalf("order detail: %d %s", code, body)
	}

	// HTMX 取消：返回状态片段。
	code, _, body = doReq(t, "POST", srv.URL+fmt.Sprintf("/admin/orders/%d/cancel", o.ID), nil, tok, true)
	if code != 200 || !strings.Contains(body, "已取消") {
		t.Fatalf("cancel HX: %d %s", code, body)
	}
	// 再次取消：400。
	code, _, _ = doReq(t, "POST", srv.URL+fmt.Sprintf("/admin/orders/%d/cancel", o.ID), nil, tok, false)
	if code != 400 {
		t.Fatalf("second cancel: %d, want 400", code)
	}
	code, _, _ = doReq(t, "GET", srv.URL+"/admin/orders/99999", nil, tok, false)
	if code != 404 {
		t.Fatalf("missing order: %d, want 404", code)
	}
}

func TestParsePriceCents(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"19.99", 1999},
		{"20", 2000},
		{"0.5", 50},
		{"0.05", 5},
		{" 7.25 ", 725},
	} {
		got, err := parsePriceCents(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("parsePriceCents(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "abc", "19.999", "-5", "1.2.3", ".5"} {
		if _, err := parsePriceCents(bad); err == nil {
			t.Fatalf("parsePriceCents(%q): want error", bad)
		}
	}
}
