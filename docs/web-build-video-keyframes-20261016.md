# Grok 视频关键帧、URL 参考图与 Web 文本音色

日期：2026-10-16，Asia/Shanghai。

## 交付状态

代码提交：`9d32af38`，已推送 `realotz/grok2api` 的 `main` 与
`codex/web-video-keyframes-voices`。**尚未部署新功能，不能视为全部验收通过。**

| 项目 | 实现 | 真实验证 |
| --- | --- | --- |
| Web 关键帧 | 已完成参数、上传资产、时间戳及任务持久化适配 | 等待新版本部署 |
| Build 关键帧 | 已完成官方请求结构及现有 Build/XAI 出站字段映射 | 无可用 Super Build 凭据，未验证 |
| URL 参考图片 | 沿用现有安全下载、上传与任务管线 | Web 6 秒视频成功，已检查 MP4 |
| 视频预设音色 `reference_audios` | 沿用现有 `voice_id` 支持 | Eve 实测失败，不标为可用 |
| Web 文本转语音及音色列表 | 新增 `grok-voice-web` 和 TTS 适配器 | 等待新版本部署 |
| Build 超 4K 提示词 | 保留原限制 | 未实测，未放开 |

生产仍是 `b62dfd3a7d7d288b0d4fef6ff54cac93d8596c40`。
GitHub 拒绝触发 `oracle-release.yml`：HTTP 422，
`Actions has been disabled for this repository`。
仓库 Actions 权限及工作流查询均显示启用，重新设置启用后仍被拒绝。
需要先解除 GitHub 对该仓库／账号的 Actions 执行限制，再运行既有发布流程。
未在生产主机编译，也未通过本地 Docker 绕过 CI。

## 参数契约

创建：`POST /v1/videos` 或 `POST /v1/videos/generations`。
两者接受 JSON 和 multipart；multipart 的 `keyframes` 是 JSON 数组字符串。

```json
{
  "model": "grok-imagine-video-1.5",
  "prompt": "The camera moves smoothly through the anchored views.",
  "duration": 6,
  "aspect_ratio": "16:9",
  "resolution": "720p",
  "keyframes": [
    {"image": {"url": "https://example.com/frame-a.jpg"}, "timestamp_s": 2.0},
    {"image": {"url": "https://example.com/frame-b.jpg"}, "timestamp_s": 4.0}
  ]
}
```

示例域名需替换为实际可公开访问的图片 URL。

- 每个关键帧的 `image` 必须且只能指定 `url` 或本服务 Files API 的 `file_id`。
- `url` 支持现有 HTTPS／图片 data URI 管线；不绕过下载的地址与大小限制。
- 最多 4 个内部关键帧；`0 < timestamp_s < duration`。
- 以 1/3 秒网格取整，不能与端点或另一关键帧占用同一槽位。
- 关键帧模式最高 720p，可以组合 `image`、`last_frame`、
  `reference_images` 和 `reference_audios`。
- 有固定帧时可以省略 `prompt`；没有固定帧的参考图／参考音频模式仍需提示词。
- 关键帧目前仅路由至 Web／Build 的 `grok-imagine-video-1.5`；
  不降级到尚未适配关键帧的 Console 渠道。
- 关键帧不能用于视频编辑或延长。账号不支持请求时长时，不能偷偷缩短时长。
- 同时提供首帧与参考图时，`<IMAGE_0>` 是首帧，
  `reference_images[0]` 对应 `<IMAGE_1>`，依次递增。
- 任务受理后只轮询原任务，不因等待超时重新生成。

Web 出站：

```json
{
  "mediaGenInput": {
    "referenceToVideo": {
      "keyframes": [
        {"asset": "同账号上传后的资产 UUID", "timestampS": 2.0}
      ]
    }
  }
}
```

Build/XAI 出站保留 `keyframes[].image` 和 `timestamp_s`。
Build 主地址沿用其现有图片字段 `image_url`；XAI 官方端点使用 `url`。
不能把 Web 的资产 UUID 直接塞进 Build 请求。

## 文本转语音与视频音色

新版本的 Web 文本转语音使用公开模型 `grok-voice-web`。
生产尚未部署此版本；以下是部署后的调用契约，不是成功实测证据。

```bash
curl "$BASE_URL/v1/tts/voices?model=grok-voice-web" \
  -H "Authorization: Bearer $API_KEY"

curl "$BASE_URL/v1/audio/speech" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-voice-web",
    "input": "Welcome. Let us create something wonderful today.",
    "voice": "从音色列表取得的 voice_id",
    "language": "en",
    "response_format": "mp3"
  }' --output speech.mp3
```

实现对齐 Web `/rest/app-chat/tts-voices` 和 `/rest/app-chat/text-to-speech`，
将官方 `result.data` 音频块解码为实际音频。
当前不提供 Web TTS 的 `with_timestamps`，也没有新增音色克隆或文本描述创建音色。
视频音色通过 `reference_audios: [{"voice_id":"eve"}]` 与 `<AUDIO_0>` 引用；
它与 `/audio/speech` 是不同能力，不能用其中一个的结果证明另一个成功。

## 实测记录

使用临时验证 Key，限制 `providerScope=["grok_web"]`，
模型路由仅允许 Web 视频路由 46。未改变用户其他 Key 的权限。

### URL 参考图：通过

正式入口：`POST /v1/videos/generations`。

```json
{
  "model": "grok-imagine-video-1.5",
  "prompt": "A slow cinematic camera movement across the mountain lake in <IMAGE_0>. Keep the mountain and lake recognizable. Gentle water ripples and natural daylight. No text.",
  "reference_images": [
    {"url": "https://images.unsplash.com/photo-1470770841072-f978cf4d019e?w=1280&q=80"}
  ],
  "duration": 6,
  "resolution": "720p",
  "aspect_ratio": "16:9"
}
```

- 任务：`video_VWqS-09zhLAYddP0Gfx_Ly6s`。
- 实际渠道：`grok_web`；账号 ID：109；状态：`completed`。
- MP4：7,845,895 字节，H.264，1280 × 720，24 fps。
- 视频容器时长：6.041667 秒；AAC 音轨：6.000000 秒。
- 已检查中间帧，山体、湖泊、木屋和倒影正常，无空白画面。
- 本地结果：`outputs/keyframes-voices-20261016/web-url-reference.mp4`。
- 中间帧：`outputs/keyframes-voices-20261016/web-url-reference-frame.jpg`。

### 视频 Eve 音色：失败

任务：`video_TWj0oHIry63EtRwGB1Tuch2X`。
参数：6 秒、720p、16:9，`reference_audios=[{"voice_id":"eve"}]`。
实际渠道：`grok_web`；账号 ID：148。

日志先报告当前 Imagine 页面缺少预设音色配置，随后上游流返回
“视频上游错误：未提供错误详情”。没有可验收音频／视频产物。
未反复生成以掩盖失败，尚不能确认是音色资产映射还是上游能力限制。

### SuperGrok 转 Build：阻塞

通过正式管理接口 `POST /api/admin/v1/accounts/web/convert-to-build`，
分别选择 SuperGrok 14046 和 14047，`strategy="missing"`。
两次均未创建或关联 Build 账号；批准 Device Flow 阶段返回 xAI OAuth 403。
随后只读检查授权页面时遇到人机验证。原 Web 账号保留，没有重置账号或强行改资格。
数据库中当前没有 `build_super_entitled=true` 的 Build 账号。

用户授权手动完成一次验证后，使用账号 14047 和正式 Device OAuth 入口，
在临时云浏览器打开官方设备登录页。页面处于已登录状态，点击“继续”后，
`accounts.x.ai/oauth2/device/consent` 返回 Cloudflare
“Sorry, you have been blocked”，没有验证码复选框或图片题。
已通过页面结构和截图确认；Ray ID：`a4b052cabe7bae17`。
这属于授权入口拦截，不能用“手动验证完成”描述。未创建 Build 账号，
本次临时浏览器与 Profile 已关闭并清理，没有改变原 SuperGrok。

### 超过 4K 提示词

原限制为 Build 主地址 **4096 UTF-8 字节**，不是 4096 个汉字。
网关总上限和 XAI 分支原有行为未改变。
没有可用 Super Build 凭据，因此未提交 >4096 字节的 Build 视频请求，
也没有放开限制。后续应固定 Build 渠道，记录 UTF-8 字节数与字符数，
先验证普通 6 秒视频，再验证 5000 个 ASCII 字符的 6 秒视频，
取得真实产物后才调整该上限。

## 代码检查与后续验收

已完成 Go 构建和相关包 `go vet`；遵循仓库要求，未新增或运行自动化测试。
真实接口验证只覆盖上表明确标为通过的项目。

恢复 CI 后：

1. 从用户 fork 的 `main` 运行 `oracle-release.yml`，核对运行容器 revision。
2. 使用限定 Web／Build 的 Key 分别验证，禁止跨渠道兜底凑成功。
3. 验证两张 URL 关键帧分别位于 2 秒、4 秒的 6 秒视频；
   下载原任务结果，核对帧位、尺寸、时长与音轨。
4. 获取 Web 音色列表并实际合成一段短音频，核验音频编码及可听内容。
5. 完成一个 SuperGrok 的官方 Build 授权后，继续 Build 关键帧和超 4K 验证。

## 对照来源

- 官方文档：<https://docs.x.ai/developers/model-capabilities/video/reference-to-video>
- 当前 Web 发布包：
  `https://cdn.grok.com/_next/static/chunks/2bbm9n6clh3_p.js`
  的 `buildReferenceToVideoInput`。
- 当前 Web 发布包：
  `https://cdn.grok.com/_next/static/chunks/0ny5h46_8u0dc.js`
  的关键帧 `asset/timestampS` 序列化与 TTS 请求／响应映射。
- 本轮从公开发布包读取实际字段；没有可用的 REA MCP/CLI，
  因此不将结果表述为 REA 工具验证。
