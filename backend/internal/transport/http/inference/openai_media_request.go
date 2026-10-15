package inference

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	mediadomain "github.com/chenyme/grok2api/backend/internal/domain/media"
	"github.com/gin-gonic/gin"
	_ "golang.org/x/image/webp"
)

const (
	maxMediaMultipartBytes = 32 << 20
	maxMediaMultipartParts = 64
	maxMediaImageDimension = 8192
	maxMediaImagePixels    = 16 << 20
)

var errUnsupportedMediaOption = errors.New("媒体选项不受支持")

// decodeOpenAIMediaRequest 有界读取 JSON 或 multipart，不落临时文件，并复用原请求类型。
func (h *Handler) decodeOpenAIMediaRequest(c *gin.Context, target any, video bool) bool {
	mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || (!strings.EqualFold(mediaType, "application/json") && !strings.EqualFold(mediaType, "multipart/form-data")) {
		writeOpenAIError(c, http.StatusUnsupportedMediaType, "invalid_request", "仅支持 application/json 或 multipart/form-data")
		return false
	}
	limit := h.maxBodyBytes
	isMultipart := strings.EqualFold(mediaType, "multipart/form-data")
	if isMultipart {
		if limit <= 0 || limit > maxMediaMultipartBytes {
			limit = maxMediaMultipartBytes
		}
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	if isMultipart {
		err = decodeMediaMultipart(c.Request.Body, params["boundary"], target, video)
	} else {
		err = decodeSingleJSON(c.Request.Body, target, video)
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeOpenAIError(c, http.StatusRequestEntityTooLarge, "request_too_large", "媒体请求或文件超过大小上限")
	case errors.Is(err, errUnsupportedMediaOption):
		writeOpenAIError(c, http.StatusBadRequest, "unsupported_parameter", err.Error())
	default:
		writeOpenAIError(c, http.StatusBadRequest, "invalid_request", "媒体请求无效: "+err.Error())
	}
	return false
}

// decodeMediaMultipart 逐段解析表单，限制段数与单文件字节，拒绝重复标量和不支持的文件字段。
func decodeMediaMultipart(body io.Reader, boundary string, target any, video bool) error {
	if boundary == "" {
		return errors.New("multipart 缺少 boundary")
	}
	reader := multipart.NewReader(body, boundary)
	fields := make(map[string]json.RawMessage)
	images := make([]imageEditJSONImage, 0)
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if count >= maxMediaMultipartParts {
			_ = part.Close()
			return fmt.Errorf("multipart 最多允许 %d 段", maxMediaMultipartParts)
		}
		name := part.FormName()
		if name == "" {
			_ = part.Close()
			return errors.New("multipart 字段名不能为空")
		}
		if name == "mask" {
			_ = part.Close()
			return fmt.Errorf("%w: 当前图片编辑不支持 mask，请使用 selection_regions 或 multi_region_edits", errUnsupportedMediaOption)
		}
		if part.FileName() != "" {
			if (video && name != "input_reference") || (!video && name != "image" && name != "image[]") {
				_ = part.Close()
				return fmt.Errorf("%w: 不支持文件字段 %s", errUnsupportedMediaOption, name)
			}
			if video {
				if _, exists := fields[name]; exists {
					_ = part.Close()
					return fmt.Errorf("%s 不能重复提供", name)
				}
			}
			dataURL, err := mediaImagePartDataURL(part)
			_ = part.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if video {
				fields[name], err = json.Marshal(videoGenerationImage{URL: dataURL})
				if err != nil {
					return err
				}
			} else {
				if len(images) >= mediadomain.MaxModelReferenceImages {
					return fmt.Errorf("图片文件不能超过 %d 张", mediadomain.MaxModelReferenceImages)
				}
				images = append(images, imageEditJSONImage{URL: dataURL})
			}
			continue
		}
		if _, exists := fields[name]; exists {
			_ = part.Close()
			return fmt.Errorf("%s 不能重复提供", name)
		}
		value, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return err
		}
		fields[name], err = mediaFormField(name, string(value), video)
		if err != nil {
			return err
		}
	}
	// multipart 的结束标记后仍可能有正文，必须计入整个请求的字节上限。
	if _, err := io.Copy(io.Discard, body); err != nil {
		return err
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if err := decodeSingleJSON(bytes.NewReader(encoded), target, video); err != nil {
		return err
	}
	if request, ok := target.(*imageEditJSONRequest); ok {
		request.Images = append(request.Images, images...)
	}
	return nil
}

// mediaImagePartDataURL 限制文件与像素预算并完整解码验证，仅保留原始图片字节。
func mediaImagePartDataURL(part *multipart.Part) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, mediadomain.MaxInputAssetBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > mediadomain.MaxInputAssetBytes {
		return "", &http.MaxBytesError{Limit: mediadomain.MaxInputAssetBytes}
	}
	if len(data) == 0 {
		return "", errors.New("图片文件不能为空")
	}
	detected := http.DetectContentType(data)
	switch detected {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return "", fmt.Errorf("%w: 仅支持 jpeg/png/webp/gif 图片，input_reference 不支持视频文件", errUnsupportedMediaOption)
	}
	if declared := part.Header.Get("Content-Type"); declared != "" {
		mediaType, _, err := mime.ParseMediaType(declared)
		if err != nil || (!strings.EqualFold(mediaType, detected) && !strings.EqualFold(mediaType, "application/octet-stream")) {
			return "", errors.New("文件 Content-Type 与图片内容不匹配")
		}
	}
	if err := validateMediaImageData(data); err != nil {
		return "", err
	}
	return "data:" + detected + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// validateMediaImageData 先校验尺寸预算再完整解码，避免压缩图片放大后耗尽内存。
func validateMediaImageData(data []byte) error {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("图片头无效: %w", err)
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxMediaImageDimension || config.Height > maxMediaImageDimension ||
		int64(config.Width)*int64(config.Height) > maxMediaImagePixels {
		return errors.New("图片尺寸不得超过 8192，像素总数不得超过 16777216")
	}
	if format == "gif" {
		if err := validateMediaGIFBudget(data); err != nil {
			return err
		}
		if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("GIF 图片内容无效: %w", err)
		}
		return nil
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("图片内容无效: %w", err)
	}
	if decodedFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return errors.New("图片解码尺寸或格式不一致")
	}
	return nil
}

// validateMediaGIFBudget 预检所有帧的累计像素，再交给标准 GIF 解码器校验内容。
func validateMediaGIFBudget(data []byte) error {
	if len(data) < 13 {
		return errors.New("GIF 图片截断")
	}
	offset := 13
	if data[10]&0x80 != 0 {
		offset += 3 * (1 << (1 + uint(data[10]&7)))
	}
	var pixels int64
	frames := 0
	for offset < len(data) {
		marker := data[offset]
		offset++
		switch marker {
		case 0x3b:
			if frames == 0 {
				return errors.New("GIF 图片没有帧")
			}
			return nil
		case 0x21:
			offset++ // 扩展类型之后是带长度的数据块。
		case 0x2c:
			if offset+9 > len(data) {
				return errors.New("GIF 帧截断")
			}
			width := int(binary.LittleEndian.Uint16(data[offset+4:]))
			height := int(binary.LittleEndian.Uint16(data[offset+6:]))
			pixels += int64(width) * int64(height)
			frames++
			if width < 1 || height < 1 || width > maxMediaImageDimension || height > maxMediaImageDimension ||
				pixels > maxMediaImagePixels || frames > 128 {
				return errors.New("GIF 超过累计像素或 128 帧预算")
			}
			packed := data[offset+8]
			offset += 9
			if packed&0x80 != 0 {
				offset += 3 * (1 << (1 + uint(packed&7)))
			}
			offset++ // 跳过 LZW 最小码长，由正式解码器校验。
		default:
			return errors.New("GIF 数据块无效")
		}
		for {
			if offset >= len(data) {
				return errors.New("GIF 数据截断")
			}
			size := int(data[offset])
			offset++
			if size == 0 {
				break
			}
			offset += size
		}
	}
	return errors.New("GIF 缺少结束标记")
}

// mediaFormField 将有支持语义的标量和结构化表单字段严格转换为原 JSON 类型。
func mediaFormField(name, value string, video bool) (json.RawMessage, error) {
	kind := ""
	switch name {
	case "model", "prompt", "size", "aspect_ratio", "resolution":
		kind = "string"
	}
	if video {
		switch name {
		case "user":
			kind = "string"
		case "duration", "seconds":
			kind = "integer"
		case "video_extension_start_time":
			kind = "number"
		case "image", "last_frame", "reference_images", "reference_audios", "keyframes", "video", "output", "storage_options", "input_reference":
			kind = "json"
		}
	} else {
		switch name {
		case "quality", "response_format":
			kind = "string"
		case "n", "partial_images":
			kind = "integer"
		case "stream":
			kind = "boolean"
		case "image", "images", "selection_regions", "multi_region_edits", "storage_options":
			kind = "json"
		case "background", "input_fidelity", "output_format", "output_compression", "user":
			return nil, fmt.Errorf("%w: 当前图片编辑不支持 %s", errUnsupportedMediaOption, name)
		}
	}
	trimmed := strings.TrimSpace(value)
	switch kind {
	case "string":
		return json.Marshal(value)
	case "integer":
		number, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s 必须是整数", name)
		}
		return json.Marshal(number)
	case "number":
		number, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("%s 必须是有限数值", name)
		}
		encoded, err := json.Marshal(number)
		if err != nil {
			return nil, fmt.Errorf("%s 必须是有限数值", name)
		}
		return encoded, nil
	case "boolean":
		if trimmed != "true" && trimmed != "false" {
			return nil, fmt.Errorf("%s 必须是 true 或 false", name)
		}
		return json.RawMessage(trimmed), nil
	case "json":
		if !json.Valid([]byte(trimmed)) {
			return nil, fmt.Errorf("%s 必须是有效 JSON", name)
		}
		return json.RawMessage(trimmed), nil
	default:
		return nil, fmt.Errorf("%w: 不支持表单字段 %s", errUnsupportedMediaOption, name)
	}
}

// validateImageEditOptions 拒绝没有底层语义的 OpenAI 选项，特别是不将像素 mask 当作参考图。
func validateImageEditOptions(request imageEditJSONRequest) error {
	for _, option := range []struct {
		name  string
		value json.RawMessage
	}{
		{"mask", request.Mask}, {"background", request.Background}, {"input_fidelity", request.InputFidelity},
		{"output_format", request.OutputFormat}, {"output_compression", request.OutputCompression}, {"user", request.User},
	} {
		if hasJSONValue(option.value) {
			return fmt.Errorf("当前图片编辑不支持 %s", option.name)
		}
	}
	return nil
}

// normalizeOpenAIVideoRequest 将标准字段映射为原生成字段，冲突或不能精确表达的选项明确报错。
func normalizeOpenAIVideoRequest(request *videoGenerationRequest, operation string) error {
	if operation != gatewayVideoOperationGenerate && (hasJSONValue(request.Seconds) || request.Size != "" || request.InputReference != nil) {
		return errors.New("seconds、size 和 input_reference 仅支持视频生成；编辑和延长请使用原字段")
	}
	if hasJSONValue(request.Seconds) {
		seconds, _, err := parseOptionalVideoInteger(request.Seconds)
		if err != nil || seconds < 1 || seconds > 15 {
			return errors.New("seconds 必须是 1 到 15 的整数或整数字符串")
		}
		if hasJSONValue(request.Duration) {
			duration, _, err := parseOptionalVideoInteger(request.Duration)
			if err != nil || duration != seconds {
				return errors.New("seconds 与 duration 不能冲突")
			}
		}
		request.Duration = request.Seconds
	}
	if request.Size != "" {
		var resolution, aspectRatio string
		switch strings.ToLower(strings.TrimSpace(request.Size)) {
		case "1280x720":
			resolution, aspectRatio = "720p", "16:9"
		case "720x1280":
			resolution, aspectRatio = "720p", "9:16"
		case "1920x1080":
			resolution, aspectRatio = "1080p", "16:9"
		case "1080x1920":
			resolution, aspectRatio = "1080p", "9:16"
		default:
			return errors.New("size 仅支持 1280x720、720x1280、1920x1080 或 1080x1920；其他尺寸无法精确映射")
		}
		if value := strings.ToLower(strings.TrimSpace(request.Resolution)); value != "" && value != resolution {
			return errors.New("size 与 resolution 不能冲突")
		}
		if value := strings.TrimSpace(request.AspectRatio); value != "" && value != aspectRatio {
			return errors.New("size 与 aspect_ratio 不能冲突")
		}
		request.Resolution, request.AspectRatio = resolution, aspectRatio
	}
	if request.InputReference != nil {
		if request.Image != nil {
			return errors.New("input_reference 与 image 不能同时提供")
		}
		request.Image = request.InputReference
	}
	return nil
}

// videoJobPixelSize 从任务保存的比例和分辨率还原整数像素尺寸，无法精确表达时不猜测。
func videoJobPixelSize(job mediadomain.Job) string {
	if !validVideoAspectRatio(job.Size) {
		return ""
	}
	var shortSide int
	switch job.Quality {
	case "480p":
		shortSide = 480
	case "720p":
		shortSide = 720
	case "1080p":
		shortSide = 1080
	default:
		return ""
	}
	widthText, heightText, _ := strings.Cut(job.Size, ":")
	widthRatio, _ := strconv.Atoi(widthText)
	heightRatio, _ := strconv.Atoi(heightText)
	divisor := min(widthRatio, heightRatio)
	if shortSide*widthRatio%divisor != 0 || shortSide*heightRatio%divisor != 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", shortSide*widthRatio/divisor, shortSide*heightRatio/divisor)
}
