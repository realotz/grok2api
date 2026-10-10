# Oracle 发布

本仓库的 `Oracle ARM64 Release` 在 GitHub `ubuntu-24.04-arm` runner 上使用
根目录 Dockerfile 编译前后端并发布 `ghcr.io/realotz/grok2api`。
镜像包含源码 revision 和源码归档 SHA256 标签。部署使用不可变 digest。

发布入口：

```sh
gh workflow run oracle-release.yml --repo realotz/grok2api --ref main
```

也可推送 `oracle-vX.Y.Z` 标签；标签指向的提交必须已在 `main` 中。
发布不会由普通分支 push 或 pull request 触发。

GitHub Secrets：

| 名称 | 用途 |
| --- | --- |
| `ORACLE_HOST` | Oracle SSH 主机 |
| `ORACLE_PORT` | SSH 端口 |
| `ORACLE_USER` | 专用 `grok2api-deploy` 账户 |
| `ORACLE_DEPLOY_SSH_KEY` | 本服务专用 SSH 私钥 |
| `ORACLE_SSH_KNOWN_HOSTS` | 已核实的 SSH 主机公钥 |

GHCR 使用 Actions 短期 `GITHUB_TOKEN`，通过 SSH stdin 传给部署端。
服务器仅在临时 Docker 配置中使用该 token，退出时删除。部署日志不输出凭据。

服务器部署脚本安装在 `/opt/grok2api-go`，归 root 所有。
首次接入由管理员运行 `provision.sh SCRIPT_DIRECTORY PUBLIC_KEY_FILE`，
核对当前 Compose 与预期一致后，安装部署脚本、专用账户与公钥。
专用账户的 SSH key 使用 `restrict` 和强制命令：

```text
sudo -n /opt/grok2api-go/ci-deploy.sh "$SSH_ORIGINAL_COMMAND"
```

sudo 仅允许这个固定脚本，脚本只接受 `deploy ghcr.io/realotz/grok2api@sha256:...`。
专用账户不加入 Docker 组。

部署沿用服务器 `/opt/grok2api-go/compose.yaml`，其中 `grok2api.image`
读取 `GROK2API_IMAGE`。原有业务配置、PostgreSQL、数据挂载、质量守护状态
和网络由服务器维护。部署只执行
`docker compose up -d --no-deps --pull never --wait grok2api`，
健康检查成功后将 digest 原子写入本服务 `.env`。

K3s 独立手动更新；Oracle workflow 不连接 K3s，不执行集群发布命令。

## 首发源码证据

2026-10-10 核对 `/opt/grok2api-go/build-fingerprint.json`：

- 线上源码基底：`2b824139e58e0c5da3fbe3332e1f3da028d5d653`。
- 线上构建输入 SHA256：
  `d8a0180253bc76fbaedb9f8203f729ff137ad5551fc861d0fd28065f501d9511`。
- 源码保留提交：`06a24a5749b80f9c09cf5d42fa6983e943b3c66f`。
- 已提交的 458 个生产构建文件逐文件 SHA256 与线上记录一致。
  未纳入发布的三项是 Statsig 调试 JSON 和两个 macOS `.DS_Store` 文件。
- x.ai 直连、Build 1.0.50 和标准媒体请求兼容源码保持线上内容；
  数据库 schema、媒体路由风控和 signer 服务没有此次实现变更。
- 线上 Compose 镜像字段接入前后，规范化配置完全一致。
- 业务配置 SHA256：
  `f88bb637dc6fade046084f05a04e8075bcb9ab774f5f4a92af6ca4c4e991d9e1`。

PostgreSQL 只读 schema 查询确认 `grok2api_go` 的 public schema 有 32 张表；
`account_credentials`、`client_keys`、`runtime_settings` 共 49 个字段。
该证据不包含发布后 API 验收；发布后须另行核对容器 digest、healthz、
`/v1/models` 和轻量 Grok 4.7 文本请求。
