# 部署示例（deploy.example）

本目录是**示例**，不是可直接上线的配置。真实部署配置放在仓库外的 `deploy/`（已 gitignore，永不提交），里面可能含内网地址等环境信息。

## 怎么用

```bash
cp -r deploy.example deploy
```

然后按自己的环境改 `deploy/` 里的两处：

1. `nginx-zhonger-go.conf`：把 `<YOUR_TAILSCALE_IP>` 换成后台监听的内网地址；把文件并入你自己域名的 nginx vhost，放在 `location /` 之前。
2. `zhonger-go.service`：按实际安装路径改 `WorkingDirectory`、`EnvironmentFile`、`ExecStart`；`User`/`Group` 改成你建的专用系统用户。

## 占位符清单

| 占位符 | 含义 |
|---|---|
| `<YOUR_TAILSCALE_IP>` | 后台管理口监听的内网地址（Tailscale 内网 IP），只走内网，不进 nginx 反代 |

## 约定

- 后台口令只走环境变量（`EnvironmentFile` 指向的 `.env`），绝不写进仓库。
- 未配口令时后台接口 fail-closed，直接返回 503。
- 公共端口只监听 `127.0.0.1:8080`，对外流量一律经 nginx 反代。
- 数据库：`ZHONGER_DB` 指向的 SQLite 文件（如 `/opt/zhonger/data/zhonger.db`）；该目录须归运行服务的专用用户所有并可写，否则启动时打不开数据库文件、服务起不来。
