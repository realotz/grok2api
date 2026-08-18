package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

type webMediaUpstreamError struct {
	status              int
	summary             string
	bodyBytes           int
	bodyTruncated       bool
	bodyPrefixSHA256    string
	bodyKind            string
	cloudflareChallenge bool
}

func (e *webMediaUpstreamError) Error() string {
	if e == nil {
		return ""
	}
	return e.summary
}

func (e *webMediaUpstreamError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

// isClearanceRefreshableMediaError distinguishes browser-session challenges
// from structured upstream policy responses such as content moderation. Empty
// and HTML 403 bodies are the forms returned by the media endpoints when the
// request is rejected before the application response is built.
func isClearanceRefreshableMediaError(e *webMediaUpstreamError) bool {
	if e == nil || e.status != http.StatusForbidden {
		return false
	}
	return e.cloudflareChallenge || e.bodyKind == "empty" || e.bodyKind == "html"
}

func (e *webMediaUpstreamError) providerResponse() *provider.Response {
	if e == nil {
		return nil
	}
	code := "upstream_forbidden"
	if e.status != http.StatusForbidden {
		code = "upstream_unavailable"
	}
	return jsonProviderResponse(e.status, map[string]any{"error": map[string]any{
		"message": e.summary,
		"type":    "upstream_error",
		"code":    code,
	}})
}

const (
	webMediaDiagnosticBodyLimit    = 64 << 10
	webMediaDiagnosticSummaryLimit = 256
	webMediaDiagnosticFieldLimit   = 160
	maxVideoReferenceAudioBytes    = 20 << 20
)

var (
	webMediaAuthorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	webMediaCookiePattern        = regexp.MustCompile(`(?i)\b(cookie|set-cookie)\b\s*[:=]\s*[^\r\n]+`)
	webMediaSecretPattern        = regexp.MustCompile(`(?i)(["']?(?:authorization|proxy-authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|upload[_-]?url|cookie|sso|session[_-]?id)["']?\s*[:=]\s*["']?)[^"'\s,;}]+`)
	webMediaJWTPattern           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}(?:\.[A-Za-z0-9_-]{12,})?\b`)
	webMediaEmailPattern         = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	webMediaURLPattern           = regexp.MustCompile(`https?://[^\s"'<>]+`)
	webMediaLongTokenPattern     = regexp.MustCompile(`[A-Za-z0-9+/=_-]{256,}`)
)

// newWebMediaUpstreamError keeps the HTTP status while exposing only a
// bounded, redacted summary through the error. Structured logs retain only
// body metadata and a prefix hash, never the upstream response body itself.
func newWebMediaUpstreamError(status int, body []byte, truncated bool) *webMediaUpstreamError {
	digest := sha256.Sum256(body)
	return &webMediaUpstreamError{
		status:              status,
		summary:             summarizeWebMediaUpstreamError(status, body, truncated),
		bodyBytes:           len(body),
		bodyTruncated:       truncated,
		bodyPrefixSHA256:    fmt.Sprintf("%x", digest),
		bodyKind:            classifyWebMediaDiagnosticBody(body),
		cloudflareChallenge: isCloudflareChallengeBody(body),
	}
}

func classifyWebMediaDiagnosticBody(body []byte) string {
	if !utf8.Valid(body) {
		return "binary"
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "empty"
	}
	if json.Valid(body) {
		return "json"
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
		return "html"
	}
	for _, value := range trimmed {
		if value < 0x20 && value != '\t' && value != '\r' && value != '\n' {
			return "binary"
		}
	}
	return "text"
}

func isCloudflareChallengeBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "just a moment") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "__cf_chl") ||
		strings.Contains(lower, "cf-chl-")
}

func (a *Adapter) logWebMediaUpstreamRejection(stage string, response *http.Response, upstreamErr *webMediaUpstreamError) {
	if upstreamErr == nil {
		return
	}
	attributes := []any{
		"stage", stage,
		"status", upstreamErr.status,
		"body_bytes_captured", upstreamErr.bodyBytes,
		"body_truncated", upstreamErr.bodyTruncated,
		"body_prefix_sha256", upstreamErr.bodyPrefixSHA256,
		"body_kind", upstreamErr.bodyKind,
		"cloudflare_challenge", upstreamErr.cloudflareChallenge,
	}
	if response != nil {
		attributes = append(attributes,
			"content_type", safeWebMediaDiagnostic(response.Header.Get("Content-Type"), 128),
			"content_length", response.ContentLength,
			"content_encoding", safeWebMediaDiagnostic(response.Header.Get("Content-Encoding"), 64),
			"server", safeWebMediaDiagnostic(response.Header.Get("Server"), 128),
			"cf_ray", safeWebMediaDiagnostic(response.Header.Get("CF-Ray"), 128),
			"upstream_request_id", safeWebMediaDiagnostic(firstNonEmpty(response.Header.Get("X-Request-Id"), response.Header.Get("X-Xai-Request-Id")), 128),
		)
	}
	a.log().Warn("web_media_upstream_rejected", attributes...)
}

func summarizeWebMediaUpstreamError(status int, body []byte, truncated bool) string {
	code, message, structured := extractWebMediaUpstreamErrorFields(body)
	parts := []string{fmt.Sprintf("Grok Web 媒体上游返回 %d", status)}
	if code != "" {
		parts = append(parts, code)
	}
	if message != "" {
		parts = append(parts, message)
	} else if len(strings.TrimSpace(string(body))) == 0 {
		parts = append(parts, "<empty>")
	} else if truncated {
		parts = append(parts, "响应正文过长")
	} else if !structured {
		parts = append(parts, "响应正文不可解析")
	} else if code == "" {
		parts = append(parts, "未提供错误详情")
	}
	return boundWebMediaDiagnostic(strings.Join(parts, ": "), webMediaDiagnosticSummaryLimit)
}

func extractWebMediaUpstreamErrorFields(body []byte) (code, message string, structured bool) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", "", false
	}
	structured = true
	if errorObject, ok := root["error"].(map[string]any); ok {
		code = firstWebMediaDiagnosticCode(errorObject, "code", "type", "error")
		message = firstString(errorObject, "message", "error", "detail")
	} else if errorText, ok := root["error"].(string); ok {
		message = errorText
	}
	if code == "" {
		code = firstWebMediaDiagnosticCode(root, "code", "error_code", "type")
	}
	if message == "" {
		message = firstString(root, "message", "error_message", "detail")
	}
	return safeWebMediaDiagnostic(code, 64), safeWebMediaDiagnostic(message, webMediaDiagnosticFieldLimit), true
}

func firstWebMediaDiagnosticCode(value map[string]any, keys ...string) string {
	if code := firstString(value, keys...); code != "" {
		return code
	}
	if code, ok := firstInt(value, keys...); ok {
		return fmt.Sprintf("%d", code)
	}
	return ""
}

func safeWebMediaDiagnostic(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	value = webMediaCookiePattern.ReplaceAllString(value, "$1: [REDACTED]")
	value = webMediaAuthorizationPattern.ReplaceAllString(value, "$1 [REDACTED]")
	value = webMediaSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	value = webMediaJWTPattern.ReplaceAllString(value, "[REDACTED]")
	value = webMediaEmailPattern.ReplaceAllString(value, "[REDACTED_EMAIL]")
	value = webMediaURLPattern.ReplaceAllString(value, "[REDACTED_URL]")
	value = webMediaLongTokenPattern.ReplaceAllString(value, "[REDACTED_LONG_VALUE]")
	return boundWebMediaDiagnostic(value, limit)
}

func boundWebMediaDiagnostic(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (a *Adapter) GenerateVideo(ctx context.Context, request provider.VideoRequest) (provider.VideoResult, error) {
	if strings.TrimSpace(request.Model) == "grok-imagine-video-1.5" {
		return a.generateVideoV15(ctx, request)
	}
	return a.generateLegacyVideo(ctx, request)
}

// generateLegacyVideo 保留原 grok-imagine-video 的 Web 请求结构。
func (a *Adapter) generateLegacyVideo(ctx context.Context, request provider.VideoRequest) (provider.VideoResult, error) {
	cfg := a.config()
	token, err := a.cipher.Decrypt(request.Credential.EncryptedAccessToken)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	lease, err := a.egress.AcquireCredential(ctx, domainegress.ScopeWeb, request.Credential)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	defer lease.Release()
	parentID := ""
	rawReferences := make([]string, 0, 1+len(request.ReferenceURLs))
	if imageURL := strings.TrimSpace(request.ImageURL); imageURL != "" {
		rawReferences = append(rawReferences, imageURL)
	}
	for _, rawReference := range request.ReferenceURLs {
		if value := strings.TrimSpace(rawReference); value != "" {
			rawReferences = append(rawReferences, value)
		}
	}
	references := make([]string, 0, len(rawReferences))
	for _, rawReference := range rawReferences {
		reference, referenceErr := a.prepareVideoReference(ctx, cfg, lease, token, rawReference)
		if referenceErr != nil {
			return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(referenceErr), 0, referenceErr)
		}
		references = append(references, reference)
	}
	if len(references) > 0 {
		parentID, err = a.createMediaPost(ctx, cfg, lease, token, "MEDIA_POST_TYPE_IMAGE", references[0], "", "video_reference_media_post")
	} else {
		parentID, err = a.createMediaPost(ctx, cfg, lease, token, "MEDIA_POST_TYPE_VIDEO", "", request.Prompt, "video_prompt_media_post")
	}
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
	}
	segments := videoSegments(request.Duration)
	if len(segments) == 0 {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, fmt.Errorf("duration 必须在 1 到 15 秒之间"))
	}
	if err := rejectUnsupportedWebFreeVideo(request, false); err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	ratio := resolveAspectRatio(request.AspectRatio)
	resolution := request.Resolution
	if resolution == "" {
		resolution = "720p"
	}
	payload := videoCreatePayload(request.Prompt, parentID, ratio, resolution, segments[0], references)
	response, err := a.postJSON(ctx, cfg, lease, token, cfg.BaseURL+"/rest/app-chat/conversations/new", payload, time.Duration(cfg.VideoTimeoutSeconds)*time.Second)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
	}
	return a.finishVideoResponse(response, request.Progress)
}

// generateVideoV15 使用 Imagine Web 的 mediaGenInput 三分支协议。
func (a *Adapter) generateVideoV15(ctx context.Context, request provider.VideoRequest) (provider.VideoResult, error) {
	cfg := a.config()
	token, err := a.cipher.Decrypt(request.Credential.EncryptedAccessToken)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	lease, err := a.egress.AcquireCredential(ctx, domainegress.ScopeWeb, request.Credential)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	defer lease.Release()
	operation := request.Operation
	if operation == "" {
		operation = provider.VideoOperationGenerate
	}
	if operation == provider.VideoOperationExtend {
		return a.extendVideoV15(ctx, cfg, lease, token, request)
	}
	if operation != provider.VideoOperationGenerate {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, fmt.Errorf("Web grok-imagine-video-1.5 不支持视频编辑"))
	}
	imageAssetID := ""
	referenceAssetIDs := make([]string, 0, len(request.ReferenceURLs))
	parentID := ""
	prepareReference := func(rawReference string) (string, error) {
		uploaded, referenceErr := a.prepareVideoAsset(ctx, cfg, lease, token, rawReference)
		if referenceErr != nil {
			return "", referenceErr
		}
		if uploaded.ID == "" {
			return "", fmt.Errorf("上传视频参考图片后未返回资产 ID")
		}
		postID, postErr := a.createMediaPost(ctx, cfg, lease, token, "MEDIA_POST_TYPE_IMAGE", uploaded.URI, "", "video_reference_media_post")
		if postErr != nil {
			return "", postErr
		}
		if parentID == "" {
			parentID = postID
		}
		return uploaded.ID, nil
	}
	if imageURL := strings.TrimSpace(request.ImageURL); imageURL != "" {
		imageAssetID, err = prepareReference(imageURL)
		if err != nil {
			return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
		}
	}
	for _, rawReference := range request.ReferenceURLs {
		if strings.TrimSpace(rawReference) == "" {
			continue
		}
		assetID, referenceErr := prepareReference(rawReference)
		if referenceErr != nil {
			return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(referenceErr), 0, referenceErr)
		}
		referenceAssetIDs = append(referenceAssetIDs, assetID)
	}
	audioAssetIDs := make([]string, 0, len(request.ReferenceAudios))
	for _, rawAudio := range request.ReferenceAudios {
		assetID, audioErr := a.prepareVideoReferenceAudio(ctx, cfg, lease, token, rawAudio)
		if audioErr != nil {
			return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(audioErr), 0, audioErr)
		}
		audioAssetIDs = append(audioAssetIDs, assetID)
	}
	segments := videoSegments(request.Duration)
	if len(segments) == 0 {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, fmt.Errorf("duration 必须在 1 到 15 秒之间"))
	}
	if err := rejectUnsupportedWebFreeVideo(request, false); err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	ratio := resolveAspectRatio(request.AspectRatio)
	resolution := resolveWebVideoV15Resolution(request.Credential.WebTier, request.Resolution)
	payload := videoV15CreatePayload(request.Prompt, ratio, resolution, segments[0], imageAssetID, referenceAssetIDs, audioAssetIDs)
	referer := cfg.BaseURL + "/imagine"
	if parentID != "" {
		referer = cfg.BaseURL + "/imagine/post/" + parentID
	}
	response, err := a.postJSONWithReferer(ctx, cfg, lease, token, cfg.BaseURL+"/rest/app-chat/conversations/new", payload, time.Duration(cfg.VideoTimeoutSeconds)*time.Second, referer)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
	}
	return a.finishVideoResponse(response, request.Progress)
}

// extendVideoV15 uploads the source video into the selected account before starting a new extension conversation.
func (a *Adapter) extendVideoV15(ctx context.Context, cfg Config, lease *egress.Lease, token string, request provider.VideoRequest) (provider.VideoResult, error) {
	if request.Duration < 6 || request.Duration > 10 {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, fmt.Errorf("Web 视频延长 duration 必须在 6 到 10 秒之间"))
	}
	if err := rejectUnsupportedWebFreeVideo(request, true); err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	if request.VideoExtensionStartTime <= 0 {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, fmt.Errorf("Web 视频延长必须提供大于 0 的 video_extension_start_time"))
	}
	video, err := a.loadVideoExtensionInput(ctx, lease, request.VideoURL, 20<<20)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, err)
	}
	uploaded, err := a.uploadFileV2Direct(ctx, cfg, lease, token, video, cfg.BaseURL+"/imagine", imagineSelfUploadSource, "video_extension_upload")
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
	}
	if uploaded.URI == "" {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePrepare, 0, fmt.Errorf("上传待延长视频后未返回 fileUri"))
	}
	postID, err := a.createMediaPost(ctx, cfg, lease, token, "MEDIA_POST_TYPE_VIDEO", uploaded.URI, "", "video_extension_media_post")
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
	}
	payload := videoExtensionPayload(request.Prompt, postID, request.Duration, request.VideoExtensionStartTime)
	response, err := a.postJSONWithReferer(
		ctx,
		cfg,
		lease,
		token,
		cfg.BaseURL+"/rest/app-chat/conversations/new",
		payload,
		time.Duration(cfg.VideoTimeoutSeconds)*time.Second,
		cfg.BaseURL+"/imagine/post/"+postID,
	)
	if err != nil {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoCreateFailureStage(err), 0, err)
	}
	return a.finishVideoResponse(response, request.Progress)
}

func (a *Adapter) loadVideoExtensionInput(ctx context.Context, lease *egress.Lease, value string, maxBytes int64) (provider.ImageInput, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return provider.ImageInput{}, fmt.Errorf("待延长视频不能为空")
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return parseVideoExtensionDataURI(value, maxBytes)
	}
	target, err := validateRemoteAttachmentURL(ctx, value, fmt.Errorf("待延长视频无效"))
	if err != nil {
		return provider.ImageInput{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	download, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.fetchURL.String(), nil)
	if err != nil {
		return provider.ImageInput{}, err
	}
	download.Host = target.hostHeader
	download.Header = remoteFileHeaders(lease.UserAgent)
	download.Header.Set("Accept", "video/mp4,application/octet-stream;q=0.8,*/*;q=0.1")
	response, err := lease.DoPinnedHTTPS(download, target.serverName)
	if err != nil {
		return provider.ImageInput{}, fmt.Errorf("下载待延长视频: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return provider.ImageInput{}, fmt.Errorf("下载待延长视频返回 %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return provider.ImageInput{}, fmt.Errorf("待延长视频超过 %d MiB", maxBytes>>20)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > maxBytes {
		return provider.ImageInput{}, fmt.Errorf("待延长视频下载失败或超过 %d MiB", maxBytes>>20)
	}
	if err := validateMP4Video(raw, response.Header.Get("Content-Type")); err != nil {
		return provider.ImageInput{}, err
	}
	filename := path.Base(target.originalURL.Path)
	if filename == "." || filename == "/" || filename == "" || !strings.EqualFold(path.Ext(filename), ".mp4") {
		filename = "video.mp4"
	}
	return provider.ImageInput{Filename: filename, MIMEType: "video/mp4", Data: raw}, nil
}

func parseVideoExtensionDataURI(value string, maxBytes int64) (provider.ImageInput, error) {
	header, encoded, ok := strings.Cut(value, ",")
	lowerHeader := strings.ToLower(header)
	if !ok || !strings.HasPrefix(lowerHeader, "data:video/mp4") || !strings.Contains(lowerHeader, ";base64") {
		return provider.ImageInput{}, fmt.Errorf("待延长视频 data URI 必须是 Base64 MP4")
	}
	encoded = strings.Join(strings.Fields(encoded), "")
	if encoded == "" || int64(base64.StdEncoding.DecodedLen(len(encoded))) > maxBytes {
		return provider.ImageInput{}, fmt.Errorf("待延长视频为空或超过 %d MiB", maxBytes>>20)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(raw) == 0 || int64(len(raw)) > maxBytes {
		return provider.ImageInput{}, fmt.Errorf("待延长视频 Base64 无效或超过 %d MiB", maxBytes>>20)
	}
	if err := validateMP4Video(raw, "video/mp4"); err != nil {
		return provider.ImageInput{}, err
	}
	return provider.ImageInput{Filename: "video.mp4", MIMEType: "video/mp4", Data: raw}, nil
}

func validateMP4Video(data []byte, declared string) error {
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	if declared != "" && declared != "application/octet-stream" && declared != "video/mp4" {
		return fmt.Errorf("待延长视频 Content-Type 必须是 video/mp4")
	}
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return fmt.Errorf("待延长视频不是有效 MP4")
	}
	return nil
}

func (a *Adapter) finishVideoResponse(response *http.Response, progress func(int)) (provider.VideoResult, error) {
	result, _, parseErr := parseVideoStream(response, progress)
	_ = response.Body.Close()
	if parseErr != nil {
		if upstreamErr, ok := parseErr.(*webMediaUpstreamError); ok {
			a.logWebMediaUpstreamRejection("video_generation", response, upstreamErr)
		}
		stage := provider.VideoStagePoll
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			stage = provider.VideoCreateFailureStage(parseErr)
		}
		return provider.VideoResult{}, provider.WrapVideoStage(stage, 0, parseErr)
	}
	if result.URL == "" {
		return provider.VideoResult{}, provider.WrapVideoStage(provider.VideoStagePoll, 0, fmt.Errorf("视频生成完成但没有返回内容 URL"))
	}
	return result, nil
}

func (a *Adapter) prepareVideoReference(ctx context.Context, cfg Config, lease *egress.Lease, token, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("视频参考图片 URL 不能为空")
	}
	image, err := a.loadChatImage(ctx, lease, value, 20<<20)
	if err != nil {
		return "", err
	}
	uploaded, err := a.uploadFileV2Direct(ctx, cfg, lease, token, image, cfg.BaseURL+"/imagine", imagineSelfUploadSource, "video_reference_upload")
	if err != nil {
		return "", err
	}
	if uploaded.URI == "" {
		return "", fmt.Errorf("上传视频参考图片后未返回 fileUri")
	}
	return uploaded.URI, nil
}

func (a *Adapter) prepareVideoAsset(ctx context.Context, cfg Config, lease *egress.Lease, token, value string) (uploadedFile, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return uploadedFile{}, fmt.Errorf("视频参考图片 URL 不能为空")
	}
	image, err := a.loadChatImage(ctx, lease, value, 20<<20)
	if err != nil {
		return uploadedFile{}, err
	}
	uploaded, err := a.uploadFileV2Direct(ctx, cfg, lease, token, image, cfg.BaseURL+"/imagine", imagineSelfUploadSource, "video_reference_upload")
	if err != nil {
		return uploadedFile{}, err
	}
	if uploaded.URI == "" {
		return uploadedFile{}, fmt.Errorf("上传视频参考图片后未返回 fileUri")
	}
	return uploaded, nil
}

// prepareVideoReferenceAudio 在视频账号的同一出口租约内上传参考音频，避免跨账号资产不可见。
func (a *Adapter) prepareVideoReferenceAudio(ctx context.Context, cfg Config, lease *egress.Lease, token, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("视频参考音频不能为空")
	}
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "data:") {
		return "", fmt.Errorf("视频参考音频必须提供 HTTPS URL 或 Base64 data URI")
	}
	audio, err := a.loadVideoReferenceAudio(ctx, lease, value)
	if err != nil {
		return "", err
	}
	uploaded, err := a.uploadFileV2Direct(ctx, cfg, lease, token, audio, cfg.BaseURL+"/imagine", selfUploadFileSource, "video_reference_audio_upload")
	if err != nil {
		return "", err
	}
	if uploaded.ID == "" {
		return "", fmt.Errorf("上传视频参考音频后未返回资产 ID")
	}
	return uploaded.ID, nil
}

func (a *Adapter) loadVideoReferenceAudio(ctx context.Context, lease *egress.Lease, value string) (provider.ImageInput, error) {
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return parseVideoReferenceAudioDataURI(value)
	}
	target, err := validateRemoteAttachmentURL(ctx, value, fmt.Errorf("视频参考音频无效"))
	if err != nil {
		return provider.ImageInput{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.fetchURL.String(), nil)
	if err != nil {
		return provider.ImageInput{}, err
	}
	request.Host = target.hostHeader
	request.Header.Set("Accept", "audio/*,*/*;q=0.1")
	request.Header.Set("User-Agent", lease.UserAgent)
	response, err := lease.DoPinnedHTTPS(request, target.serverName)
	if err != nil {
		return provider.ImageInput{}, fmt.Errorf("下载视频参考音频: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return provider.ImageInput{}, fmt.Errorf("下载视频参考音频返回 %d", response.StatusCode)
	}
	if response.ContentLength > maxVideoReferenceAudioBytes {
		return provider.ImageInput{}, fmt.Errorf("视频参考音频超过 20 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxVideoReferenceAudioBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxVideoReferenceAudioBytes {
		return provider.ImageInput{}, fmt.Errorf("视频参考音频下载失败或超过 20 MiB")
	}
	mimeType, err := validatedVideoReferenceAudioMIME(raw, response.Header.Get("Content-Type"), path.Ext(target.originalURL.Path))
	if err != nil {
		return provider.ImageInput{}, err
	}
	return provider.ImageInput{Filename: "reference" + videoReferenceAudioExtension(mimeType), MIMEType: mimeType, Data: raw}, nil
}

func parseVideoReferenceAudioDataURI(value string) (provider.ImageInput, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(strings.ToLower(header), "data:audio/") || !strings.Contains(strings.ToLower(header), ";base64") {
		return provider.ImageInput{}, fmt.Errorf("视频参考音频必须是 Base64 audio data URI")
	}
	encoded = strings.Join(strings.Fields(encoded), "")
	if encoded == "" || int64(base64.StdEncoding.DecodedLen(len(encoded))) > maxVideoReferenceAudioBytes {
		return provider.ImageInput{}, fmt.Errorf("视频参考音频为空或超过 20 MiB")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(raw) == 0 || len(raw) > maxVideoReferenceAudioBytes {
		return provider.ImageInput{}, fmt.Errorf("视频参考音频 Base64 无效或超过 20 MiB")
	}
	declared := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(header), "data:"), ";base64"))
	mimeType, err := validatedVideoReferenceAudioMIME(raw, declared, "")
	if err != nil {
		return provider.ImageInput{}, err
	}
	return provider.ImageInput{Filename: "reference" + videoReferenceAudioExtension(mimeType), MIMEType: mimeType, Data: raw}, nil
}

func validatedVideoReferenceAudioMIME(data []byte, declared, extension string) (string, error) {
	detected := normalizeVideoReferenceAudioMIME(http.DetectContentType(data))
	declared = normalizeVideoReferenceAudioMIME(declared)
	if declared == "" {
		declared = videoReferenceAudioMIMEFromExtension(extension)
	}
	if !supportedVideoReferenceAudioMIME(declared) {
		declared = detected
	}
	if !supportedVideoReferenceAudioMIME(declared) {
		return "", fmt.Errorf("视频参考音频格式不支持")
	}
	if supportedVideoReferenceAudioMIME(detected) && detected != declared {
		return "", fmt.Errorf("视频参考音频 Content-Type 与实际内容不一致")
	}
	return declared, nil
}

func normalizeVideoReferenceAudioMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	switch value {
	case "audio/x-wav", "audio/wave":
		return "audio/wav"
	case "application/octet-stream":
		return ""
	default:
		return value
	}
}

func supportedVideoReferenceAudioMIME(value string) bool {
	switch value {
	case "audio/mpeg", "audio/wav", "audio/mp4", "audio/aac", "audio/ogg", "audio/webm", "audio/flac":
		return true
	default:
		return false
	}
}

func videoReferenceAudioMIMEFromExtension(extension string) string {
	switch strings.ToLower(extension) {
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".webm":
		return "audio/webm"
	case ".flac":
		return "audio/flac"
	default:
		return ""
	}
}

func videoReferenceAudioExtension(mimeType string) string {
	switch mimeType {
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "audio/mp4":
		return ".m4a"
	case "audio/aac":
		return ".aac"
	case "audio/ogg":
		return ".ogg"
	case "audio/webm":
		return ".webm"
	case "audio/flac":
		return ".flac"
	default:
		return ".audio"
	}
}

// DownloadVideo retrieves a completed Grok asset through its source SSO
// session. Direct asset URLs are not public and must not be exposed as a
// substitute for this authenticated transfer.
func (a *Adapter) DownloadVideo(ctx context.Context, credential account.Credential, rawURL string) (io.ReadCloser, string, int64, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || !trustedImageAssetHost(parsed.Hostname()) || parsed.User != nil {
		return nil, "", 0, fmt.Errorf("视频内容 URL 不受信任")
	}
	token, err := a.cipher.Decrypt(credential.EncryptedAccessToken)
	if err != nil {
		return nil, "", 0, err
	}
	// 视频生成与成品下载必须复用同一账号身份；否则 Resin 会为 WebAsset
	// 重新分配租约，账号级 Cloudflare clearance 也不会进入下载请求。
	lease, err := a.egress.AcquireCredential(ctx, domainegress.ScopeWebAsset, credential)
	if err != nil {
		return nil, "", 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		lease.Release()
		return nil, "", 0, err
	}
	request.Header = buildHeaders(token, lease, "")
	request.Header.Del("Content-Type")
	response, err := lease.Do(request)
	if err != nil {
		a.egress.FeedbackForScope(context.WithoutCancel(ctx), domainegress.ScopeWebAsset, lease.NodeID, 0, err)
		lease.Release()
		return nil, "", 0, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		a.egress.FeedbackForScope(context.WithoutCancel(ctx), domainegress.ScopeWebAsset, lease.NodeID, response.StatusCode, nil)
		lease.Release()
		return nil, "", 0, fmt.Errorf("下载视频返回 %d", response.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = "video/mp4"
	}
	if !strings.HasPrefix(contentType, "video/") {
		_ = response.Body.Close()
		lease.Release()
		return nil, "", 0, fmt.Errorf("上游视频 Content-Type 无效")
	}
	onFinished := func(readErr error, complete bool) {
		if readErr != nil {
			a.egress.FeedbackForScope(context.WithoutCancel(ctx), domainegress.ScopeWebAsset, lease.NodeID, 0, readErr)
		} else if complete {
			a.egress.FeedbackForScope(context.WithoutCancel(ctx), domainegress.ScopeWebAsset, lease.NodeID, response.StatusCode, nil)
		}
		lease.Release()
	}
	return provider.NewCompletionReadCloser(response.Body, onFinished), contentType, response.ContentLength, nil
}

func parseVideoStream(response *http.Response, progress func(int)) (provider.VideoResult, string, error) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, webMediaDiagnosticBodyLimit+1))
		if response.StatusCode == http.StatusUnauthorized {
			return provider.VideoResult{}, "", provider.ErrUnauthorized
		}
		truncated := len(body) > webMediaDiagnosticBodyLimit
		if truncated {
			body = body[:webMediaDiagnosticBodyLimit]
		}
		return provider.VideoResult{}, "", newWebMediaUpstreamError(response.StatusCode, body, truncated)
	}
	var result provider.VideoResult
	var postID string
	handle := func(root map[string]any) (bool, error) {
		if errorValue, ok := root["error"].(map[string]any); ok {
			return false, webMediaStreamError(errorValue)
		}
		if errorValue := nestedMap(root, "result", "response", "error"); errorValue != nil {
			return false, webMediaStreamError(errorValue)
		}
		stream := nestedMap(root, "result", "response", "streamingVideoGenerationResponse")
		if stream != nil {
			if value, ok := numberAsInt(stream["progress"]); ok && progress != nil {
				progress(value)
			}
			if value, _ := stream["videoPostId"].(string); value != "" {
				postID = value
			} else if value, _ := stream["videoId"].(string); value != "" {
				postID = value
			}
			moderated, _ := stream["moderated"].(bool)
			if moderated {
				return false, nil
			}
			if setVideoResultURL(&result, firstString(stream, "videoUrl", "contentUrl", "contentURL", "assetUrl", "assetURL", "fileUri", "fileURL")) {
				return true, nil
			}
		}
		for _, attachment := range videoFileAttachments(root) {
			if setVideoResultURL(&result, attachment) {
				return true, nil
			}
		}
		return false, nil
	}

	reader := bufio.NewReader(response.Body)
	prefix, _ := reader.Peek(64)
	trimmedPrefix := strings.TrimSpace(string(prefix))
	var err error
	if strings.HasPrefix(trimmedPrefix, "data:") || strings.HasPrefix(trimmedPrefix, "event:") {
		err = consumeVideoSSE(reader, handle)
	} else {
		err = consumeVideoJSON(reader, handle)
	}
	if err != nil {
		return provider.VideoResult{}, "", err
	}
	return result, postID, nil
}

func webMediaStreamError(value map[string]any) error {
	message := safeWebMediaDiagnostic(firstString(value, "message", "error", "detail"), webMediaDiagnosticFieldLimit)
	if message == "" {
		message = "未提供错误详情"
	}
	return fmt.Errorf("视频上游错误: %s", message)
}

func videoFileAttachments(root map[string]any) []string {
	modelResponse := nestedMap(root, "result", "response", "modelResponse")
	if modelResponse == nil {
		return nil
	}
	values, _ := modelResponse["fileAttachments"].([]any)
	attachments := make([]string, 0, len(values))
	for _, value := range values {
		if attachment, _ := value.(string); attachment != "" {
			attachments = append(attachments, attachment)
		}
	}
	// Official Imagine now also finishes via fileAttachmentAssetMetadata
	// (mimeType=video/* plus key/hdKey) when streamingVideoGenerationResponse.videoUrl
	// is omitted. Treat those the same as fileAttachments.
	metadata, _ := modelResponse["fileAttachmentAssetMetadata"].([]any)
	for _, value := range metadata {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		mimeType := strings.ToLower(strings.TrimSpace(firstString(item, "mimeType")))
		if mimeType != "" && !strings.HasPrefix(mimeType, "video/") {
			continue
		}
		if candidate := firstString(item, "key", "hdKey", "hd1080Key", "fileUri", "fileURL"); candidate != "" {
			attachments = append(attachments, candidate)
		}
	}
	return attachments
}

func setVideoResultURL(result *provider.VideoResult, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	pathOnly := strings.SplitN(lower, "?", 2)[0]
	if !strings.HasSuffix(pathOnly, ".mp4") && !strings.Contains(lower, "/content") && !strings.HasPrefix(strings.TrimPrefix(pathOnly, "https://assets.grok.com/"), "users/") {
		return false
	}
	result.URL = absoluteAssetURL(value)
	result.ContentType = "video/mp4"
	return true
}

func consumeVideoSSE(reader io.Reader, handle func(map[string]any) (bool, error)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" || line == "[DONE]" || !strings.HasPrefix(line, "{") {
			continue
		}
		var root map[string]any
		if json.Unmarshal([]byte(line), &root) != nil {
			continue
		}
		complete, err := handle(root)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
	}
	return scanner.Err()
}

func consumeVideoJSON(reader io.Reader, handle func(map[string]any) (bool, error)) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<20))
	for {
		var root map[string]any
		if err := decoder.Decode(&root); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("解析视频上游流: %w", err)
		}
		complete, err := handle(root)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
	}
}

func nestedMap(value map[string]any, keys ...string) map[string]any {
	current := value
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func videoSegments(seconds int) []int {
	if seconds < 1 || seconds > 15 {
		return nil
	}
	return []int{seconds}
}

func isWebFreeVideoTier(tier account.WebTier) bool {
	return tier == "" || tier == account.WebTierAuto || tier == account.WebTierBasic
}

func rejectUnsupportedWebFreeVideo(request provider.VideoRequest, extend bool) error {
	if !isWebFreeVideoTier(request.Credential.WebTier) {
		return nil
	}
	if request.Duration != 6 {
		if extend {
			return fmt.Errorf("免费 Web 账号视频延长仅支持 6 秒")
		}
		return fmt.Errorf("免费 Web 账号仅支持 6 秒视频")
	}
	if strings.EqualFold(strings.TrimSpace(request.Resolution), "1080p") {
		return fmt.Errorf("免费 Web 账号不支持 1080p")
	}
	return nil
}

// resolveWebVideoV15Resolution matches grok.com Imagine: default 480p.
// Free/Basic 仅 480p/720p；1080p 只给 Super/Heavy。
func resolveWebVideoV15Resolution(tier account.WebTier, requested string) string {
	value := strings.ToLower(strings.TrimSpace(requested))
	if value == "" {
		value = "480p"
	}
	if value == "720p" {
		return "720p"
	}
	switch tier {
	case account.WebTierSuper, account.WebTierHeavy:
		return value
	default:
		return "480p"
	}
}

func videoCreatePayload(prompt, parentID, ratio, resolution string, seconds int, references []string) map[string]any {
	config := map[string]any{"parentPostId": parentID, "aspectRatio": ratio, "videoLength": seconds, "resolutionName": resolution}
	if len(references) > 0 {
		config["isVideoEdit"] = false
		config["isReferenceToVideo"] = true
		config["imageReferences"] = references
	}
	return map[string]any{
		"temporary": true, "modelName": "imagine-video-gen", "message": prompt + " --mode=custom", "enableSideBySide": true,
		"responseMetadata": map[string]any{"experiments": []any{}, "modelConfigOverride": map[string]any{"modelMap": map[string]any{"videoGenModelConfig": config}}},
	}
}

// videoV15CreatePayload 按 Imagine Web 新协议区分文生、单图和多参考图视频。
func videoV15CreatePayload(prompt, ratio, resolution string, seconds int, imageAssetID string, referenceAssetIDs, audioAssetIDs []string) map[string]any {
	parameters := map[string]any{"prompt": prompt, "aspectRatio": ratio, "duration": seconds, "resolutionName": resolution}
	mediaGenInput := map[string]any{}
	switch {
	case imageAssetID != "":
		parameters["inputAssets"] = []string{imageAssetID}
		mediaGenInput["imageToVideo"] = parameters
	case len(referenceAssetIDs) > 0 || len(audioAssetIDs) > 0:
		if len(referenceAssetIDs) > 0 {
			parameters["inputAssets"] = referenceAssetIDs
		}
		if len(audioAssetIDs) > 0 {
			parameters["audioAssets"] = audioAssetIDs
		}
		mediaGenInput["referenceToVideo"] = parameters
	default:
		mediaGenInput["textToVideo"] = parameters
	}
	return map[string]any{
		"modelName": "imagine-video-gen", "message": prompt + " --mode=custom",
		"enableImageStreaming": true, "enableSideBySide": true, "sendFinalMetadata": true,
		"responseMetadata": map[string]any{"experiments": []any{}, "modelConfigOverride": map[string]any{"modelMap": map[string]any{}}},
		"mediaGenInput":    mediaGenInput,
		"kind":             "CONVERSATION_KIND_IMAGINE",
	}
}

// videoExtensionPayload maps the OpenAI-compatible extension request to Grok Web videoExtension.
func videoExtensionPayload(prompt, postID string, duration int, startTime float64) map[string]any {
	parameters := map[string]any{
		"inputAssets":             []string{postID},
		"duration":                duration,
		"videoExtensionStartTime": startTime,
	}
	if prompt != "" {
		parameters["prompt"] = prompt
	}
	message := "--mode=normal"
	if prompt != "" {
		message = prompt + " --mode=custom"
	}
	return map[string]any{
		"message":              message,
		"modelName":            "imagine-video-gen",
		"enableImageStreaming": true,
		"enableSideBySide":     true,
		"sendFinalMetadata":    true,
		"mediaGenInput": map[string]any{
			"videoExtension": parameters,
		},
	}
}
