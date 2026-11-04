# 贡献指南

欢迎提 issue 和 PR。改动请附上验证说明（怎么跑、怎么测）。

## 本地开发

环境：Go 1.27，依赖 chi v5。

```bash
export PATH=~/sdk/go/bin:$PATH
cd app
go build -o /tmp/zhonger-go ./cmd/server
ZHONGER_ADDR=127.0.0.1:8080 ZHONGER_ADMIN_ADDR=127.0.0.1:8081 \
  ZHONGER_ADMIN_TOKEN=test-token /tmp/zhonger-go
curl http://127.0.0.1:8080/api/healthz
curl -H "Authorization: Bearer test-token" http://127.0.0.1:8081/admin/api/status
```

## 不要提交的东西

- 真实口令、密钥、Token（后台口令只走环境变量 `ZHONGER_ADMIN_TOKEN`）
- 内网地址、服务器真实 IP、个人隐私信息
- `deploy.example/` 里的配置是通用部署模板，部署到自己的服务器前请按自己环境修改（真实配置放 `deploy/`，已 gitignore，永不提交）
