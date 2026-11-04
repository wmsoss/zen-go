package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustProduct(t *testing.T, s *Store, slug string, price int64, active bool) *Product {
	t.Helper()
	p := &Product{Slug: slug, Name: "商品 " + slug, Description: "desc", PriceCents: price, Currency: "USD", Active: active}
	if err := s.CreateProduct(p); err != nil {
		t.Fatalf("CreateProduct(%s): %v", slug, err)
	}
	return p
}

func TestProductCRUD(t *testing.T) {
	s := openTestStore(t)
	p := mustProduct(t, s, "alpha", 1999, true)

	got, err := s.GetProduct(p.ID)
	if err != nil || got.Name != p.Name || got.PriceCents != 1999 {
		t.Fatalf("GetProduct: %+v, %v", got, err)
	}
	bySlug, err := s.GetProductBySlug("alpha")
	if err != nil || bySlug.ID != p.ID {
		t.Fatalf("GetProductBySlug: %+v, %v", bySlug, err)
	}

	// slug 冲突。
	dup := &Product{Slug: "alpha", Name: "dup", PriceCents: 100, Currency: "USD", Active: true}
	if err := s.CreateProduct(dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate slug: err = %v, want ErrConflict", err)
	}

	// 校验：坏 slug、坏价格、坏币种。
	for _, bad := range []*Product{
		{Slug: "Bad Slug", Name: "x", PriceCents: 100, Currency: "USD"},
		{Slug: "ok", Name: "x", PriceCents: 0, Currency: "USD"},
		{Slug: "ok", Name: "x", PriceCents: 100, Currency: "usd"},
		{Slug: "ok", Name: "", PriceCents: 100, Currency: "USD"},
	} {
		if err := s.CreateProduct(bad); !errors.Is(err, ErrValidation) {
			t.Fatalf("CreateProduct(%+v): err = %v, want ErrValidation", bad, err)
		}
	}

	// 更新 + 唯一性。
	p.Name = "改名"
	p.PriceCents = 2999
	if err := s.UpdateProduct(p); err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	other := mustProduct(t, s, "beta", 500, true)
	other.Slug = "alpha"
	if err := s.UpdateProduct(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdateProduct slug conflict: err = %v, want ErrConflict", err)
	}
	if err := s.UpdateProduct(&Product{ID: 99999, Slug: "ghost", Name: "g", PriceCents: 100, Currency: "USD"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateProduct missing: err = %v, want ErrNotFound", err)
	}

	// 上架/下架与列表过滤。
	mustProduct(t, s, "gamma", 100, false)
	all, err := s.ListProducts(false)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListProducts(false): n=%d, %v", len(all), err)
	}
	active, err := s.ListProducts(true)
	if err != nil || len(active) != 2 {
		t.Fatalf("ListProducts(true): n=%d, %v", len(active), err)
	}
	if err := s.SetProductActive(p.ID, false); err != nil {
		t.Fatalf("SetProductActive: %v", err)
	}
	if got, _ := s.GetProduct(p.ID); got.Active {
		t.Fatalf("SetProductActive(false) did not stick")
	}
	if err := s.SetProductActive(99999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetProductActive missing: err = %v, want ErrNotFound", err)
	}
}

func TestOrderLifecycle(t *testing.T) {
	s := openTestStore(t)
	p := mustProduct(t, s, "book", 5000, true)

	o, err := s.CreateOrder(p.ID, "buyer@example.com", 2)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if o.Status != StatusPendingPayment || o.TotalCents() != 10000 {
		t.Fatalf("order = %+v, want pending_payment total 10000", o)
	}
	if o.ProductName != p.Name || o.PriceCents != p.PriceCents {
		t.Fatalf("order snapshot wrong: %+v", o)
	}

	// 改价不影响已有订单快照。
	p.PriceCents = 6000
	if err := s.UpdateProduct(p); err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	got, _ := s.GetOrder(o.ID)
	if got.PriceCents != 5000 {
		t.Fatalf("snapshot changed after repricing: %d", got.PriceCents)
	}

	// 非法输入。
	for _, tc := range []struct {
		email string
		qty   int
	}{
		{"not-an-email", 1},
		{"a@b", 1},
		{"", 1},
		{"buyer@example.com", 0},
		{"buyer@example.com", -1},
		{"buyer@example.com", MaxQuantity + 1},
	} {
		if _, err := s.CreateOrder(p.ID, tc.email, tc.qty); !errors.Is(err, ErrValidation) {
			t.Fatalf("CreateOrder(%q, %d): err = %v, want ErrValidation", tc.email, tc.qty, err)
		}
	}
	if _, err := s.CreateOrder(99999, "buyer@example.com", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateOrder missing product: err = %v, want ErrNotFound", err)
	}
	inactive := mustProduct(t, s, "off", 100, false)
	if _, err := s.CreateOrder(inactive.ID, "buyer@example.com", 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("CreateOrder inactive product: err = %v, want ErrValidation", err)
	}

	// 取消：仅一次有效。
	if err := s.CancelOrder(o.ID); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got, _ := s.GetOrder(o.ID); got.Status != StatusCancelled {
		t.Fatalf("status after cancel = %s", got.Status)
	}
	if err := s.CancelOrder(o.ID); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("second CancelOrder: err = %v, want ErrInvalidStatus", err)
	}
	if err := s.CancelOrder(99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CancelOrder missing: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetOrder(99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetOrder missing: err = %v, want ErrNotFound", err)
	}

	orders, err := s.ListOrders()
	if err != nil || len(orders) != 1 {
		t.Fatalf("ListOrders: n=%d, %v", len(orders), err)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, bad := range []string{"", "plain", "a@b", "a@b.", "Bob <a@b.com>", "a@@b.com"} {
		if err := ValidateEmail(bad); err == nil {
			t.Fatalf("ValidateEmail(%q): want error", bad)
		}
	}
	if err := ValidateEmail("user+tag@example.co.uk"); err != nil {
		t.Fatalf("ValidateEmail(valid): %v", err)
	}
}
