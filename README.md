# zen-go

Hugo + Go 架构下一人公司站点的 Go 应用层：内容层走 Hugo 静态，商城、AI 玩具、后台管理走这个 Go 单体，同域名多入口（`/` Hugo、`/shop/*`、`/lab/*`、`/api/*`）。

线上跑在 zhong-er.com（博客“中二青年”），是作者自用并持续维护的开源项目。

## 现在有什么（M2：商城 + 订单，支付留空）

- 健康检查 `GET /api/healthz`
- 商城：商品列表 `GET /shop`、商品详情 `GET /shop/{slug}`、下单（邮箱 + 数量）`POST /shop/{slug}/buy`
- 订单确认页 `GET /orders/{id}`：订单停在 `pending_payment`，页面明示尚未接入支付、不会扣款
- 支付缝：结账 `POST /api/orders/{id}/checkout` 与回调 `POST /api/webhooks/payment` 一律 `501 payment not configured`（见“支付”一节）
- AI 玩具占位页 `/lab`
- 后台 `/admin/`：只监听内网地址 + Bearer 口令，未配口令时直接 503（fail-closed）；商品 CRUD、订单列表/查看/取消

技术栈：Go 1.27 + chi v5 + SQLite（modernc.org/sqlite，纯 Go，无需 cgo），标准库为主。

## 路线图

- M2：商城功能（商品、订单）+ 支付接口占位（已完成；支付模块留空，订单停在 `pending_payment`）
- M3：密钥自动发货 + 售后状态机 + 接入支付商
- M4：AI 玩具 + 运维面板

## 本地开发

```bash
export PATH=~/sdk/go/bin:$PATH
cd app
go build -o /tmp/zhonger-go ./cmd/server
ZHONGER_ADDR=127.0.0.1:8080 ZHONGER_ADMIN_ADDR=127.0.0.1:8081 \
  ZHONGER_ADMIN_TOKEN=test-token ZHONGER_DB=/tmp/zhonger.db /tmp/zhonger-go
curl http://127.0.0.1:8080/api/healthz
curl -H "Authorization: Bearer test-token" http://127.0.0.1:8081/admin/api/status
```

测试：`go test ./...`（SQLite 用临时文件，无需外部服务）。

## 配置

| 环境变量 | 说明 |
|---|---|
| `ZHONGER_ADDR` | 公共监听地址（如 `127.0.0.1:8080`，经 nginx 反代） |
| `ZHONGER_ADMIN_ADDR` | 后台监听地址（只绑内网，不进公网反代） |
| `ZHONGER_ADMIN_TOKEN` | 后台 Bearer 口令，只走环境变量，绝不进仓库 |
| `ZHONGER_DB` | SQLite 文件路径，默认 `./zhonger.db`（systemd 下即 `/opt/zhonger/zhonger.db`；启动时自动建表） |

## 支付：留空（stub）

支付模块按决定留空、先跳过，不接任何支付商 SDK 与密钥：

- 订单状态只有 `pending_payment` 与 `cancelled`，没有 paid 路径；下单后订单停在 `pending_payment`。
- 代码里的支付缝是 `internal/payment` 的 `Provider` 接口（`CreateCheckout` + `HandleWebhook`），唯一实现是 `NoopProvider`，两个方法都返回“payment not configured”，对应端点返回 501。
- 以后接入支付商（Lemon / Creem / Dodo / PayPal 方向待定）时，在 `internal/payment` 新增一个实现 `Provider` 的类型，并在 `main.go` 里选用它；状态机随回调扩展。

## 部署

systemd + nginx 反代，`deploy.example/` 里有通用部署模板（真实配置放 `deploy/`，已 gitignore），部署到自己的服务器前请按自己环境修改。两个注意点：

- 动态 location 必须写 `proxy_cache off`，否则缓存可能把 HEAD 转成 GET，导致签名类接口 403
- 后台 `/admin` 故意不进公网反代，只走内网

## 许可证

MIT，见 `LICENSE`。欢迎按 `CONTRIBUTING.md` 参与。
