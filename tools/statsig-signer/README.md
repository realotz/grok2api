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

默认本机 Playwright。可选 `--browser x2api` 走 x2api Browser Worker gRPC。正常更新复用一个浏览器，以两个独立页面的并发批量采样；只加载脚本、样式和接口，阻止无关图片、视频与字体，不提前逐个下载签名 chunk。

```bash
export GROK_SSO=...          # 或 STATSIG_SECRETS=/tmp/g2a-local-test/browser.secrets.json
python3 -m statsig_signer capture --browser local
python3 -m statsig_signer update --browser local
```

`update` 顺序：

1. 采集 8 组同页 seed / 官方 HEX / curves，完整枚举并批量筛选全部下标候选，不只选第一个匹配。
2. 下标唯一后，用第 9 组独立 seed 检查候选 JS 和 70 字节签名壳；通过才写热代码、公式和 pair。
3. 抓包不完整、版本在采样中变化、下标仍有歧义或独立验签不匹配，都明确报错，不写猜测结果。
4. 全部下标候选均不匹配时，才走 **Hermes 最小内核**检查官方模块的算法结构，复用已有样本。

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

默认每轮完成后等待 60 秒：普通轮次只请求 `/imagine` HTML 检查指纹，每五轮用独立页面主动对照官方 HEX。指纹变化立即启动批量修复，不必等下一次深度验签；HTML 无变化但官方 HEX 不匹配也会修复，并立即记录 `statsig_repair_started`。`--deep-every 0` 仅检查 HTML 指纹，不是默认值；需要每轮主动验签可设为 1。不要用最后 12 个 chunk 名，也不要把 HTML 脚本列表和 Playwright 抓到的 signer URL 交叉比较。

## 测试

```bash
cd tools/statsig-signer
PYTHONPATH=. python3 -m unittest discover -s tests -v
```
