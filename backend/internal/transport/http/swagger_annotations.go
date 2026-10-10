package httpserver

// SwaggerMessage 表示 Chat Completions 请求中的一条消息。
type SwaggerMessage struct {
	Role    string `json:"role" example:"user"`
	Content any    `json:"content"`
}

// SwaggerResponsesRequest 表示最小 Responses 请求。
type SwaggerResponsesRequest struct {
	Model              string `json:"model" example:"grok-chat-auto"`
	Input              any    `json:"input"`
	Stream             bool   `json:"stream" example:"false"`
	Store              bool   `json:"store" example:"false"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	PromptCacheKey     string `json:"prompt_cache_key,omitempty"`
}

// SwaggerChatRequest 表示最小 Chat Completions 请求。
type SwaggerChatRequest struct {
	Model    string           `json:"model" example:"grok-chat-fast"`
	Messages []SwaggerMessage `json:"messages"`
	Stream   bool             `json:"stream" example:"false"`
}

// SwaggerMessagesRequest 表示最小 Anthropic Messages 请求。
type SwaggerMessagesRequest struct {
	Model     string           `json:"model" example:"grok-chat-expert"`
	MaxTokens int              `json:"max_tokens" example:"1024"`
	Messages  []SwaggerMessage `json:"messages"`
	Stream    bool             `json:"stream" example:"false"`
}

// SwaggerImageGenerationRequest 表示图片生成请求。
type SwaggerImageGenerationRequest struct {
	Model          string `json:"model" example:"grok-imagine-image-quality"`
	Prompt         string `json:"prompt" example:"A cinematic city at night"`
	N              int    `json:"n" example:"1"`
	AspectRatio    string `json:"aspect_ratio,omitempty" example:"16:9"`
	Resolution     string `json:"resolution,omitempty" example:"2k"`
	ResponseFormat string `json:"response_format,omitempty" example:"url"`
	Stream         bool   `json:"stream,omitempty" example:"false"`
	PartialImages  int    `json:"partial_images,omitempty" example:"0"`
}

// SwaggerImageReference 表示图片 URL 输入。
type SwaggerImageReference struct {
	URL string `json:"url" example:"https://example.com/source.png"`
}

// SwaggerImageEditRequest 表示图片编辑请求。
type SwaggerImageEditRequest struct {
	Model          string                `json:"model" example:"grok-imagine-image-2.0-web"`
	Prompt         string                `json:"prompt" example:"Change the background to black"`
	Image          SwaggerImageReference `json:"image"`
	N              int                   `json:"n" example:"1"`
	Size           string                `json:"size,omitempty" example:"1024x1024"`
	AspectRatio    string                `json:"aspect_ratio,omitempty" example:"1:1"`
	Resolution     string                `json:"resolution,omitempty" example:"1k"`
	Quality        string                `json:"quality,omitempty" example:"medium"`
	ResponseFormat string                `json:"response_format,omitempty" example:"url"`
	Stream         bool                  `json:"stream,omitempty" example:"false"`
	PartialImages  int                   `json:"partial_images,omitempty" example:"0"`
}

// SwaggerVideoGenerationRequest 表示视频生成请求。
// grok-imagine-video-1.5 可组合 image、last_frame 与 reference_images；参考模式 resolution 最高 720p。
// reference_audios：voice_id 与 Build 一致（如 eve）；Web 还会接受 url 或 data。prompt 使用官方 <IMAGE_n>/<AUDIO_n> 标签（不区分大小写）。
type SwaggerVideoGenerationRequest struct {
	Model                   string                   `json:"model" example:"grok-imagine-video-1.5"`
	Prompt                  string                   `json:"prompt" example:"A cinematic tracking shot in the rain"`
	Duration                int                      `json:"duration" example:"8"`
	Seconds                 string                   `json:"seconds,omitempty" example:"8"`
	Size                    string                   `json:"size,omitempty" example:"1280x720"`
	InputReference          *SwaggerVideoMediaInput  `json:"input_reference,omitempty"`
	AspectRatio             string                   `json:"aspect_ratio,omitempty" example:"16:9"`
	Resolution              string                   `json:"resolution,omitempty" example:"720p"`
	Image                   *SwaggerVideoMediaInput  `json:"image,omitempty"`
	LastFrame               *SwaggerVideoMediaInput  `json:"last_frame,omitempty"`
	ReferenceImages         []SwaggerVideoMediaInput `json:"reference_images,omitempty"`
	ReferenceAudios         []SwaggerVideoAudioInput `json:"reference_audios,omitempty"`
	Video                   *SwaggerVideoMediaInput  `json:"video,omitempty"`
	VideoExtensionStartTime float64                  `json:"video_extension_start_time,omitempty" example:"6.041667"`
}

// SwaggerVideoMediaInput 表示视频相关的图片/视频输入。
type SwaggerVideoMediaInput struct {
	URL    string `json:"url,omitempty" example:"https://example.com/input.png"`
	FileID string `json:"file_id,omitempty" example:"file_123"`
}

// SwaggerVideoAudioInput 表示参考音频输入。voice_id 与 Build 一致；Web 还可提供 HTTPS url 或 Base64 data。
type SwaggerVideoAudioInput struct {
	URL     string `json:"url,omitempty" example:"https://example.com/reference.mp3"`
	Data    string `json:"data,omitempty" example:"SUQzBAAAAAAA..."`
	Format  string `json:"format,omitempty" example:"mp3"`
	VoiceID string `json:"voice_id,omitempty" example:"eve"`
}

// swaggerHealth godoc
// @Summary 存活检查
// @Tags System
// @Produce json
// @Success 200 {object} map[string]bool
// @Router /healthz [get]
func swaggerHealth() {}

// swaggerReady godoc
// @Summary 就绪检查
// @Tags System
// @Produce json
// @Success 200 {object} map[string]bool
// @Failure 503 {object} map[string]bool
// @Router /readyz [get]
func swaggerReady() {}

// swaggerModels godoc
// @Summary 获取可用模型
// @Tags Models
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]any
// @Failure 401 {object} map[string]any
// @Router /v1/models [get]
func swaggerModels() {}

// swaggerResponses godoc
// @Summary 创建 Response
// @Description 支持 JSON 与 SSE；stream=true 时返回 text/event-stream。
// @Tags Responses
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerResponsesRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Failure 401 {object} map[string]any
// @Router /v1/responses [post]
func swaggerResponses() {}

// swaggerCompactResponse godoc
// @Summary 压缩 Response 上下文
// @Tags Responses
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerResponsesRequest true "请求"
// @Success 200 {object} map[string]any
// @Router /v1/responses/compact [post]
func swaggerCompactResponse() {}

// swaggerGetResponse godoc
// @Summary 查询 Response
// @Tags Responses
// @Security BearerAuth
// @Produce json
// @Param response_id path string true "Response ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /v1/responses/{response_id} [get]
func swaggerGetResponse() {}

// swaggerDeleteResponse godoc
// @Summary 删除 Response
// @Tags Responses
// @Security BearerAuth
// @Produce json
// @Param response_id path string true "Response ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /v1/responses/{response_id} [delete]
func swaggerDeleteResponse() {}

// swaggerChat godoc
// @Summary 创建 Chat Completion
// @Description 支持 JSON 与 SSE、图片输入和函数工具。
// @Tags Chat
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerChatRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/chat/completions [post]
func swaggerChat() {}

// swaggerMessages godoc
// @Summary 创建 Anthropic Message
// @Tags Messages
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param anthropic-version header string true "Anthropic API version" default(2023-06-01)
// @Param request body SwaggerMessagesRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/messages [post]
func swaggerMessages() {}

// swaggerGenerateImage godoc
// @Summary 生成图片
// @Tags Images
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerImageGenerationRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/images/generations [post]
func swaggerGenerateImage() {}

// swaggerEditImage godoc
// @Summary 编辑图片
// @Description 支持原 JSON image/images URL 对象（包括 data URL），或 multipart 的 image/image[] 文件。multipart 标量 n、stream、partial_images 等进入同一业务校验；selection_regions/multi_region_edits 使用 JSON 表单值。每文件最多 20 MiB，multipart 总计最多 32 MiB、64 段，且不超过服务端请求体限制。n 仅支持 1，quality 仅支持 low/medium。mask、background、input_fidelity、output_format、output_compression、user 不支持并明确报错。
// @Tags Images
// @Security BearerAuth
// @Accept json,mpfd
// @Produce json
// @Param request body SwaggerImageEditRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/images/edits [post]
func swaggerEditImage() {}

// SwaggerImageLayerRequest 表示图片分层请求。
type SwaggerImageLayerRequest struct {
	Model string                `json:"model" example:"grok-imagine-image-2.0-web"`
	Image SwaggerImageReference `json:"image"`
}

// swaggerDetectImageLayers godoc
// @Summary 识别图片分层
// @Tags Images
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerImageLayerRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/images/layers [post]
func swaggerDetectImageLayers() {}

// swaggerGetImage godoc
// @Summary 获取归档图片
// @Tags Images
// @Produce image/png
// @Param asset_id path string true "Asset ID"
// @Success 200 {file} binary
// @Failure 404
// @Router /v1/media/images/{asset_id} [get]
func swaggerGetImage() {}

// swaggerGenerateVideo godoc
// @Summary 创建异步视频任务
// @Description JSON 和 multipart 共用校验。seconds（整数字符串或整数）映射 duration；size 支持 1280x720、720x1280、1920x1080、1080x1920，映射 resolution/aspect_ratio，冲突时报错。multipart input_reference 仅支持图片文件，JSON 使用与 image 相同的 url/file_id 对象；与 image 不能同时提供。每文件最多 20 MiB，multipart 总计最多 32 MiB、64 段，且不超过服务端请求体限制。保留原 reference_images/last_frame/reference_audios 字段；结构化表单值使用 JSON。POST /v1/videos 返回标准 video 对象，扩展创建路径返回 request_id。GET 返回标准 queued/in_progress/completed/failed 状态与任务真实元数据，完成时保留 video.url 和 content_url。无视频 list/delete/cancel 接口。
// @Tags Videos
// @Security BearerAuth
// @Accept json,mpfd
// @Produce json
// @Param request body SwaggerVideoGenerationRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/videos [post]
// @Router /v1/videos/generations [post]
func swaggerGenerateVideo() {}

// swaggerEditVideo godoc
// @Summary 创建异步视频编辑任务
// @Tags Videos
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerVideoGenerationRequest true "请求"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Router /v1/videos/edits [post]
func swaggerEditVideo() {}

// swaggerExtendVideo godoc
// @Summary 创建异步视频延长任务
// @Tags Videos
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerVideoGenerationRequest true "请求"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Router /v1/videos/extensions [post]
func swaggerExtendVideo() {}

// swaggerGetVideo godoc
// @Summary 查询异步视频任务
// @Tags Videos
// @Security BearerAuth
// @Produce json
// @Param request_id path string true "Request ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /v1/videos/{request_id} [get]
func swaggerGetVideo() {}
