# Grok 视频关键帧、URL 参考图与 Web 文本音色

日期：2026-10-16，Asia/Shanghai。

## 交付状态

关键帧与 TTS 基础提交：`9d32af38`，已推送 `realotz/grok2api` 的 `main` 与
`codex/web-video-keyframes-voices`。**尚未部署新功能，不能视为全部验收通过。**

| 项目 | 实现 | 真实验证 |
| --- | --- | --- |
| Web 关键帧 | 已完成参数、上传资产、时间戳及任务持久化适配 | 等待新版本部署 |
| Build 关键帧 | 已完成官方请求结构及现有 Build/XAI 出站字段映射 | 同账号上游请求完成；4 秒画面符合参考，2 秒有过渡重影，精确锚点尚未通过完整验收 |
| URL 参考图片 | 沿用现有安全下载、上传与任务管线 | Web 6 秒视频成功，已检查 MP4 |
| 视频预设音色 `reference_audios` | 沿用现有 `voice_id` 支持 | Eve 实测失败，不标为可用 |
| Web 文本转语音及音色列表 | 新增 `grok-voice-web` 和 TTS 适配器 | 等待新版本部署 |
| Build 超 4K 提示词 | 删除独有 4096 字节门槛，与 Web 共用 100,000 UTF-8 字节上限 | 5000 ASCII 字符及 10,000 中文字符均生成成功 |

生产仍是 `b62dfd3a7d7d288b0d4fef6ff54cac93d8596c40`。
GitHub 拒绝触发 `oracle-release.yml`：HTTP 422，
`Actions has been disabled for this repository`。
仓库 Actions 权限及工作流查询均显示启用。Actions 页面明确显示该 fork
因 GitHub Actions 使用规模被暂停，需要仓库维护者重新启用。
REST 仓库权限设置和工作流 enable 接口没有解除该暂停；
当前浏览器仅登录 `yuzimu`，没有 `realotz` 的维护者网页会话。
需要在网页登录 `realotz` 并恢复该 fork 的工作流，再运行既有发布流程。
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
- Build 与 Web 共用网关 `100,000` UTF-8 字节提示词上限，不截断。
  上游已实测到 10,000 个中文字符／30,000 字节，不代表 100,000 字节已实测。
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

### SuperGrok 转 Build：本地授权成功

通过正式管理接口 `POST /api/admin/v1/accounts/web/convert-to-build`，
分别选择 SuperGrok 14046 和 14047，`strategy="missing"`。
两次均未创建或关联 Build 账号；批准 Device Flow 阶段返回 xAI OAuth 403。
随后只读检查授权页面时遇到人机验证。原 Web 账号保留，没有重置账号或强行改资格。
这两次自动转换没有产生 Build 账号。

用户授权手动完成一次验证后，使用账号 14047 和正式 Device OAuth 入口，
在临时云浏览器打开官方设备登录页。页面处于已登录状态，点击“继续”后，
`accounts.x.ai/oauth2/device/consent` 返回 Cloudflare
“Sorry, you have been blocked”，没有验证码复选框或图片题。
已通过页面结构和截图确认；Ray ID：`a4b052cabe7bae17`。
这属于授权入口拦截，不能用“手动验证完成”描述。未创建 Build 账号，
本次临时浏览器与 Profile 已关闭并清理，没有改变原 SuperGrok。

随后按用户要求，在本地隔离 Chrome 中使用同一账号 14047 的 SSO，
通过正式 Device OAuth 登录及授权。SSO 只经内存传输，不写入源码或文档。
官方页面显示“设备已授权”，服务器正式轮询接口完成加密入库：

- 新 Build 账号 ID：**14049**，`authStatus=active`、`enabled=true`。
- 原 Web 账号 ID：**14047**，关联已建立，原账号保留。
- 普通 6 秒视频经正式管理接口成功，任务 `video_A-OlG68x39C9v-AvASJgfa1C`。
- 视频资产：`vid_G4YCiAz7OFV1YfLDEVNZWJQllJu48Yyq`。
- 本地临时浏览器已关闭。

### Build 关键帧上游验证

固定使用 Build 账号 14049，直接访问既有 Build 主地址
`https://cli-chat-proxy.grok.com/v1/videos/generations`，
沿用该账号 OAuth 和现有 Build 请求头及出口，没有降级到 XAI／Web。
两张图片分别使用前述湖景 URL，以及
`https://images.unsplash.com/photo-1501785888041-af3ef285b470?w=1280&q=80`，
时间戳 2 秒与 4 秒，`duration=6`、720p、16:9。
Build 主地址每项图片使用 `image: {"image_url":"..."}`。

任务 `15e6ac06-77b8-959d-a48a-f70269274c1b` 完成，MP4 11,255,687 字节，
1280×720、24fps、H.264＋AAC，视频 6.041667 秒、音轨 6 秒。
4 秒抽帧呈现第二张湖景参考；2 秒抽帧存在过渡混合，
所以只能确认请求受理并生成视频，不能声称精确关键帧一致性已全部通过。
本地结果：`outputs/keyframes-voices-20261016/build-keyframes.mp4`。

### 超过 4K 提示词

原限制为 Build 主地址 **4096 UTF-8 字节**，不是 4096 个汉字。
使用新 Build 账号 14049，直接访问上述 Build 主地址，未经过旧网关的 4096 字节拦截，
也未切换供应商，分别完成：

| 提示词 | 字符数 | UTF-8 字节 | 上游任务 | 结果 |
| --- | --- | --- | --- | --- |
| 英文陶瓷杯场景 | 5000 | 5000 | `7559555e-90ec-98b5-9c5a-2a33281a0f35` | done / 100，MP4 3,279,207 字节 |
| 中文陶瓷杯场景 | 10000 | 30000 | `6c848681-7ac5-98f6-a439-bd88adf70e6b` | done / 100，MP4 2,466,386 字节 |

中文提示词由以下同一场景描述重复并截取到恰好 10,000 个 Unicode 字符构成，
用来验证长度，不将重复文字视为复杂提示词语义服从度验证：

> 明亮的摄影棚里，一只白色陶瓷杯放在整洁的木桌上，镜头缓慢向前移动，窗外的自然光照亮杯身，细腻的纹理和柔和的阴影保持真实，背景安静且稳定，不出现文字。

10,000 字符成片已下载并用 ffprobe 核验：1280×720、24fps、H.264＋AAC，
容器 6.041667 秒，AAC 音轨 6 秒；中间帧为正常陶瓷杯场景。
产物：`outputs/keyframes-voices-20261016/build-prompt-10000-chars.mp4`。

已按用户要求删除 Build 独有的 4K 限制，与 Web 共用
`media.MaxVideoPromptBytes=100000`。线上旧镜像尚未更新，
因此“上游支持”与“生产网关限制已生效”必须分开；后者仍受 CI 发布阻塞。

## 代码检查与后续验收

已完成 Go 构建和相关包 `go vet`；遵循仓库要求，未新增或运行自动化测试。
真实接口验证只覆盖上表明确标为通过的项目。

恢复 CI 后：

1. 从用户 fork 的 `main` 运行 `oracle-release.yml`，核对运行容器 revision。
2. 使用限定 Web／Build 的 Key 分别验证，禁止跨渠道兜底凑成功。
3. 验证两张 URL 关键帧分别位于 2 秒、4 秒的 6 秒视频；
   下载原任务结果，核对帧位、尺寸、时长与音轨。
4. 获取 Web 音色列表并实际合成一段短音频，核验音频编码及可听内容。
5. 使用已创建的 Build 账号 14049，通过新网关再复验长提示词，
   并继续检查关键帧准确时间点，不重复转换账号。

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
