package payment

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestNoopProvider(t *testing.T) {
	var p Provider = NoopProvider{}
	if p.Name() != "noop" {
		t.Fatalf("Name() = %q", p.Name())
	}
	if _, err := p.CreateCheckout(context.Background(), OrderRef{ID: 1, PriceCents: 100}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("CreateCheckout: err = %v, want ErrNotConfigured", err)
	}
	req := httptest.NewRequest("POST", "/api/webhooks/payment", nil)
	if err := p.HandleWebhook(context.Background(), req); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("HandleWebhook: err = %v, want ErrNotConfigured", err)
	}
}
