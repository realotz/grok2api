---
name: statsig-hex-repair
description: >
  Repair grok.com x-statsig-id after a frontend deploy. Use whenever
  Web 403 code 7 / anti-bot persists, 1645e3 chunk names changed, or the
  user says 重新抓签名模块 / 修复 Statsig HEX / 1645e3 发版 / 对照官方签名.
  Do not use for quota 429, account blocks, or Cloudflare HTML 403.
---

# Repair grok Statsig HEX

生产签名走独立 Python 签名器 `tools/statsig-signer`，协议对齐 `grok.wodf.de/sign`。
HEX 算法是 `hot/hex.js` 里的 `computeHex(seed, paths)`。签名器常驻一个 Node 进程，
**源码变了才 vm.eval 编译一次**，之后每次 `/sign` 只调用已编译函数。不要改 Go，
不要把 Playwright 接到生产热路径。

`refreshStatsigPair` 保持空操作。grok2api URL 模式应把 `StatsigSignerURL` 指到
`http://127.0.0.1:8788/sign`。

## Agent 怎么修

不是改 `statsig_hex.go`。也不是只改 JSON 数字。

1. Agent 自己 `capture_page`（`local` 或 `x2api`）钩 digest / `animate(4096)`，抓同一页 seed、官方 HEX、4 条 curves。
2. `recover_indices` 穷举下标。对得上就把返回的 `hex_js` `write_hot_js`。
3. 下标穷举失败：`fetch_chunk` 看混淆 JS，改 `computeHex` 源码。
4. `eval_hot_js` 用 Node vm.eval 跑候选源码。**只有结果等于官方 HEX 才能 `write_hot_js`。**
5. `verify_signature`：热代码 HEX 和 70 字节壳都对才接受。**必须再抓一页新鲜对照**（`/` 与 `/imagine` 换页），不能拿 `write_hot_js` 用过的那次 seed 充数。新鲜页对不上就不能 exit。Hermes 最多 200 轮（`STATSIG_AGENT_MAX_TURNS`）。
6. 一次 seed 反解会碰巧对上；两页碰巧对上但 seek 下标不唯一时 `recover_indices` 返回 `ambiguous`，换页再抓，不要 `fetch_chunk` 空转。
7. 工具链（抓包、指纹、eval）坏了可以 `read_py` / `write_py` 改 `statsig_signer/*.py`、`tests/*.py`、`hot/prelude.js`。语法错或单测失败会回滚。不要改 Go，不要用 `write_py` 改 `hot/hex.js`。当前进程里已绑定的函数要等下次 watch tick。

前端小改（curves / seed 下标）：`recover_indices` 通常一轮就能写回 `computeHex`。
前端大改（取样方式、HEX 编码、chunk 换名）：agent 用 `find_signer_chunk` + `fetch_chunk` 对着混淆源码改 `computeHex`，eval 对上官方 HEX 才写入。如果不再是 SVG+toString(16)+70 字节壳，验不过就不会接受，不能假装修成功。

```bash
cd tools/statsig-signer
python3 -m statsig_signer serve --listen 127.0.0.1:8788

export GROK_SSO=...   # 或 STATSIG_SECRETS
export GROK2API_BASE=http://127.0.0.1:18000/v1
export GROK2API_KEY=...
export GROK2API_MODEL=grok-4.6
python3 -m statsig_signer update --browser local
python3 -m statsig_signer update --defer-capture   # 让 agent 自己 capture_page
python3 -m statsig_signer watch --once             # HTML 指纹：sentry SHA / curves / chunks 哈希
python3 -m statsig_signer watch --interval 60 --repair
```

用已有抓包测 Hermes：

```bash
python3 -m statsig_signer update --fixture data/pair.json --force-hermes
```

## 性能

| 做法 | 延迟 |
|---|---|
| 每个 `/sign` spawn 一次 Node | 差（几十到上百 ms） |
| 常驻 worker + 源码不变只调函数 | HEX 计算微秒到亚毫秒 |
| grok2api 签名缓存 | 同一 method/path 默认 1 小时 |

生产必须用常驻 worker，不要 `node -e` 每个请求。

## 前端监测

`watch` 默认每 60 秒 GET `https://grok.com/imagine`（无 ETag，必须拉全文）。对照 `sentry-release` git SHA、HTML 里的 curves 哈希、全部 chunk 文件名哈希。这三项在 `/` 和 `/imagine` 上 sentry/curves 一致；chunk 集合按页会差 1 个，所以探针固定 `/imagine`。不要用最后 12 个文件名，也不要拿 HTML 脚本列表去对 Playwright 的 signer URL。`--repair` 只在指纹变化时跑 agent；agent 自己抓包验 HEX。`--deep` 仍可强制浏览器对官方 HEX。

## 当前 computeHex（2026-09-09）

`hot/hex.js`：路径 `seed[5]%4`，段 `seed[39]%16`，seek `(seed[3],31,36)%16` 乘积对齐到 10，时长 4096。
HEX 用 JS `Number#toString(16)`。旧 aurora 下标不要回退。

## 不要做

- 不要让 Agent 改 `statsig_hex.go` / `statsig_local.go`。
- 不要为了过测试改期望 HEX，或用另一页 seed 配这一页 curves。
- 不要提交 SSO、`cf_clearance`、client key。
- 不要恢复 `x-anonuserid` / `x-challenge` / `x-signature`。
- 不要把 Playwright 做成生产签名器。
