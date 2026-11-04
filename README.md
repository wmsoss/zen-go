# zen-go

Hugo + Go 架构下一人公司站点的 Go 应用层：内容层走 Hugo 静态，商城、AI 玩具、后台管理走这个 Go 单体，同域名多入口（`/` Hugo、`/shop/*`、`/lab/*`、`/api/*`）。

线上跑在 zhong-er.com（博客“中二青年”），是作者自用并持续维护的开源项目。

## 现在有什么（M1 骨架）

- 健康检查 `GET /api/healthz`
- 商城占位页 `/shop`、AI 玩具占位页 `/lab`
- 后台骨架 `/admin/`：只监听内网地址 + Bearer 口令，未配口令时直接 503（fail-closed）

技术栈：Go 1.27 + chi v5，标准库为主。

## 路线图

- M2：商城功能（商品、订单）+ 支付接口
- M3：密钥自动发货 + 售后状态机
- M4：AI 玩具 + 运维面板

## 本地开发

```bash
export PATH=~/sdk/go/bin:$PATH
cd app
go build -o /tmp/zhonger-go ./cmd/server
ZHONGER_ADDR=127.0.0.1:8080 ZHONGER_ADMIN_ADDR=127.0.0.1:8081 \
  ZHONGER_ADMIN_TOKEN=test-token /tmp/zhonger-go
curl http://127.0.0.1:8080/api/healthz
curl -H "Authorization: Bearer test-token" http://127.0.0.1:8081/admin/api/status
```

## 配置

| 环境变量 | 说明 |
|---|---|
| `ZHONGER_ADDR` | 公共监听地址（如 `127.0.0.1:8080`，经 nginx 反代） |
| `ZHONGER_ADMIN_ADDR` | 后台监听地址（只绑内网，不进公网反代） |
| `ZHONGER_ADMIN_TOKEN` | 后台 Bearer 口令，只走环境变量，绝不进仓库 |

## 部署

systemd + nginx 反代，`deploy.example/` 里有通用部署模板（真实配置放 `deploy/`，已 gitignore），部署到自己的服务器前请按自己环境修改。两个注意点：

- 动态 location 必须写 `proxy_cache off`，否则缓存可能把 HEAD 转成 GET，导致签名类接口 403
- 后台 `/admin` 故意不进公网反代，只走内网

## 许可证

MIT，见 `LICENSE`。欢迎按 `CONTRIBUTING.md` 参与。
