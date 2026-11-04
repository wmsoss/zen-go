// Package payment: 支付商接入缝（payment seam）。
//
// M2 决定：支付模块留空、先跳过。本包只定义接口与一个空实现
// NoopProvider，任何结账尝试都返回 501 + "payment not configured"。
// 不要在这里接任何支付商 SDK、密钥或回调地址。
//
// 以后接入支付商（Lemon / Creem / Dodo / PayPal 方向待定）时：
//  1. 在本包新增一个实现 Provider 接口的类型（只加文件，不改接口）；
//  2. 在 main.go 里按配置选择该实现（当前写死 NoopProvider）；
//  3. 订单状态机随支付商回调扩展（paid 等状态属于那时的工作，
//     不在本包提前加）。
package payment

import (
	"context"
	"errors"
	"net/http"
)

// ErrNotConfigured: 未接入任何支付商。订单停在 pending_payment，
// 这是预期行为，不是故障。
var ErrNotConfigured = errors.New("payment not configured: no payment provider is connected; orders stop at pending_payment")

// OrderRef: 支付商创建结账会话所需的订单快照（只读，不含敏感信息）。
type OrderRef struct {
	ID          int64
	ProductName string
	PriceCents  int64
	Currency    string
	BuyerEmail  string
	Quantity    int
}

// TotalCents: 订单总价（分）。
func (o OrderRef) TotalCents() int64 { return o.PriceCents * int64(o.Quantity) }

// Provider: 支付商接口。CreateCheckout 返回支付商侧的结账 URL；
// HandleWebhook 处理支付商的异步回调（验签、幂等由具体实现负责）。
type Provider interface {
	Name() string
	CreateCheckout(ctx context.Context, order OrderRef) (checkoutURL string, err error)
	HandleWebhook(ctx context.Context, r *http.Request) error
}

// NoopProvider: 空实现。未接入支付商时的唯一实现，两个方法都
// 返回 ErrNotConfigured，调用方据此返回 501。
type NoopProvider struct{}

// Name: 实现名。
func (NoopProvider) Name() string { return "noop" }

// CreateCheckout: 未接入，永远失败。
func (NoopProvider) CreateCheckout(_ context.Context, _ OrderRef) (string, error) {
	return "", ErrNotConfigured
}

// HandleWebhook: 未接入，永远失败。
func (NoopProvider) HandleWebhook(_ context.Context, _ *http.Request) error {
	return ErrNotConfigured
}
