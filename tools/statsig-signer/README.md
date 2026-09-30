# Statsig signer

独立 Python 签名器，协议对齐 `https://grok.wodf.de/sign`。HEX 算法是 `hot/hex.js`，常驻 Node `vm.eval`（源码变了才重新编译）。grok2api 把 `StatsigSignerURL` 指到 `http://127.0.0.1:8788/sign`。不改 Go，不把 SSO 传给签名器。

## 协议

```http
POST /sign
Content-Type: application/json

{
  "method": "POST",
  "path": "/rest/app-chat/conversations/new",
  "environment": { "metaContent": "<grok-site-verification seed>" }
}
```

```json
{ "x-statsig-id": "<70-byte shell, base64 raw>" }
```

`metaContent` 是 48 字节 seed。签名器用当前 `data/pair.json` 里的 4 条 curves 和 `data/formula.json` 下标算 HEX，再套 70 字节壳（epoch `1682924400`，salt `obfiowerehiring`，末字节 `0x03`）。不覆盖全局 pair。

grok2api 允许的内网地址：`http://127.0.0.1:8788/sign`。

## 运行

```bash
cd tools/statsig-signer
python3 -m statsig_signer serve --listen 127.0.0.1:8788
```

健康检查：`GET /health`。agent 写完 `data/*.json` 后进程按文件 mtime 热加载。

一次性签名：

```bash
python3 -m statsig_signer sign \
  --method POST \
  --path /rest/app-chat/conversations/new \
  --meta '62/GxIkYy2AWvW+igufpMkTf0VEYX4X8Pf4awTv5dWxpQeEFtcYt+MGmcGCsgGrM'
```

## 发版更新

默认本机 Playwright。可选 `--browser x2api` 走 x2api Browser Worker gRPC。

```bash
export GROK_SSO=...          # 或 STATSIG_SECRETS=/tmp/g2a-local-test/browser.secrets.json
python3 -m statsig_signer capture --browser local
python3 -m statsig_signer update --browser local
```

`update` 顺序：

1. 抓同一页 seed / 官方 HEX / curves
2. 当前公式能对上官方 HEX → 只写 `pair.json`
3. 穷举下标能对上 → 写 `formula.json` + `pair.json`
4. 对不上 → **Hermes 最小内核**（grok2api `/v1/chat/completions` 工具循环）读 chunk、试公式，只有 HEX 匹配才 apply

```bash
export GROK2API_BASE=http://127.0.0.1:18000/v1
export GROK2API_KEY=...
export GROK2API_MODEL=grok-4.7
python3 -m statsig_signer update --browser local
```

x2api：

```bash
export X2API_ROOT=/Users/real/Documents/code/x2api
export X2API_BROWSER_ADDR=127.0.0.1:50051
python3 -m statsig_signer update --browser x2api
```

不要把 Playwright / x2api 接到生产签名路径。生产只跑 `serve`。

## 发版监测

grok.com HTML 里已经有发版标记，不必每轮开浏览器：

- `meta baggage` 的 `sentry-release=grok-web@<git sha>`（发版必变）
- RSC 里的 `\"curves\":`（4 组 SVG 控制点）
- `cdn.grok.com/_next/static/chunks/*.js` 文件名集合的哈希（内容哈希命名，JS 一变就变）

```bash
python3 -m statsig_signer watch --interval 60 --repair
```

默认 60 秒 GET `/imagine`。sentry / curves / chunks 任一变化才跑 Hermes。不要用最后 12 个 chunk 名，也不要把 HTML 脚本列表和 Playwright 抓到的 signer URL 交叉比较。`--deep-every 0` 表示识别只靠 HTML；HEX 对错由 repair 自己抓包验证。

## 测试

```bash
cd tools/statsig-signer
PYTHONPATH=. python3 -m unittest discover -s tests -v
```
