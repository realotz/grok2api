---
name: statsig-hex-repair
description: >
  Repair grok.com x-statsig-id HEX after a frontend deploy. Use whenever
  Web 403 code 7 / anti-bot persists after curves refresh, 1645e3 or 38asg
  chunk names changed, x-statsig-id algorithm needs updating, or the user
  says 重新抓签名模块 / 修复 Statsig HEX / 1645e3 发版 / 对照官方签名.
  Do not use for quota 429, account blocks, or Cloudflare HTML 403.
---

# Repair grok Statsig HEX (browser vs 1645e3)

`seed` 和页面 `curves` 会自动刷新。这个技能只处理 **公式层**：签名模块换了 seed 下标或取样方式。模块数字 ID（曾叫 `1645e3`）和 chunk 文件名（曾叫 `38asg_axwuaew.js`）发版就会变，不要按旧名字找。

## 先判断要不要动公式

1. 看日志有没有 `web_statsig_svg_paths_refreshed` + `web_statsig_pair_refreshed`。
2. 若刷新被跳过（`skipped_stale_svg`）：先修提取（RSC `"curves"` 形状），不要改下标。
3. 若配对已刷新仍 403 code 7：按下面抓浏览器、对公式。

`manual` Statsig 模式不会走现算，先排除。

## 当前写死的公式（2026-08-18 对照）

文件：`backend/internal/infra/provider/web/statsig_hex.go`

| 用途 | 下标 |
|---|---|
| 路径 | `seed[5] % 4` |
| 段 | `seed[12] % 16` |
| seek | `(seed[8]%16)*(seed[20]%16)*(seed[29]%16)`，再 `round(x/10)*10` |
| 时长 | `4096` |
| HEX | RGB + 矩阵 `(cos,sin,-sin,cos,0,0)`，各 `toFixed(2)` + JS `toString(16)`，去掉 `.`/`-` |

旧 aurora：`seed[5]%16` + `seed[22/23/24]`。不要回退。

70 字节壳（epoch `1682924400`、salt `obfiowerehiring`、末字节 `0x03`）一般不用动，在 `statsig_local.go`。

## 稳定特征（用来找新模块）

不要搜 `1645e3`。按这个顺序：

1. 首页已加载的 chunk 里搜 `x-statsig-id`（设头的那个文件）。
2. 看它异步 `import` / `e.A(数字)` 拉下来的 chunk。
3. 新 chunk 里搜 `obfiowerehiring`、`animate`、`4096`、`getComputedStyle`、`currentTime`。
4. 源码形态：`X=W=>{ let[f,d]=[W[?]%16, W[?]%16 * W[?]%16 * W[?]%16] }`，然后 `N(el, segments[f], d)`。

## 浏览器对照（同一页）

用技能脚本，不要手写新钩子（除非脚本失效）：

```bash
# 密钥文件不要提交。字段：local16_sso, proxy_server, proxy_username_tmpl, proxy_password, local17_sticky
# 或缺省读 /tmp/g2a-local-test/browser.secrets.json
node .grok/skills/statsig-hex-repair/scripts/capture_statsig_signer.mjs
```

脚本会打开 `https://grok.com/imagine`，钩 digest + `animate(4096)`，从同一份 HTML 抽 `"curves"`，写出：

- `backend/internal/infra/provider/web/testdata/statsig_live_pair.json`（seed + 4 条路径 + 官方 HEX）
- 旁边一份 `*.debug.json`（currentTime、keyframes、签名 chunk URL，不含 SSO）

要求：seed、4 条 `M 10,30 C` 路径、官方 HEX **必须来自同一次页面加载**。

本地缺 Playwright / 代理隧道时：先修 `127.0.0.1:13805`（或当前出口）和 Chromium，再抓。不要把 SSO、代理密码写进仓库或技能输出。

## 改代码

1. 用 debug 里的 `currentTime` 和 `seed` 字节核对 seek 公式；用 keyframe 的起止 RGB/`rotate(Ndeg)` 核对段号。
2. 只改 `statsig_hex.go` 里三个下标（或取样公式）。同步改文件头注释。
3. 覆盖 `testdata/statsig_live_pair.json`。
4. 跑：

```bash
cd backend && go test ./internal/infra/provider/web -count=1 -run 'TestComputeStatsigHEXMatchesLiveSamePageCapture|TestStatsigNumberToHexMatchesJS'
```

官方 HEX 必须等于 `computeStatsigHEXForSeedWithPaths(seed, paths)`。对不上就继续拆新模块，不要放宽测试。

5. 可选：重启本地 grok2api，打一条 `grok-imagine-video-1.5` 480p 确认没有 code 7。

## 不要做

- 不要把 Playwright 做成生产签名器。
- 不要为了过测试改期望 HEX，或用另一页的 seed 配这一页的路径。
- 不要提交 `browser.secrets.json`、SSO、`cf_clearance`。
- 不要恢复 `x-anonuserid` / `x-challenge` / `x-signature`。
