package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

const (
	modelProbeTextPrompt = `Return only a JSON object with this exact shape: {"ok":true,"probe":"account-test"}. No markdown, no extra text.`
	// ModelProbeDefaultMediaPrompt 是图片/视频探测的默认提示词。
	ModelProbeDefaultMediaPrompt = "小猫在天上"
	modelProbeMaxBodyBytes       = 4 << 20
)

type modelCapabilitySyncer interface {
	SyncAccount(ctx context.Context, accountID uint64) (int, error)
}

// ModelProbeOutcome 描述单次模型探测结果。
type ModelProbeOutcome string

const (
	ModelProbeOutcomeOK      ModelProbeOutcome = "ok"
	ModelProbeOutcomeFlagged ModelProbeOutcome = "flagged"
	ModelProbeOutcomeError   ModelProbeOutcome = "error"
)

// AccountModelProbeItem 是账号可用的一条可测路由。
type AccountModelProbeItem struct {
	PublicID      string
	UpstreamModel string
	Capability    modeldomain.Capability
}

// AccountModelProbeResult 是一次模型探测的结构化结果。
type AccountModelProbeResult struct {
	Outcome    ModelProbeOutcome
	PublicID   string
	Capability modeldomain.Capability
	Text       string
	PreviewURL string
	Error      string
}

// ListAccountTestModels 列出该账号可测的文本、图片、视频模型。
func (s *Service) ListAccountTestModels(ctx context.Context, accountID uint64) ([]AccountModelProbeItem, error) {
	if accountID == 0 {
		return nil, invalidInput("账号 ID 无效")
	}
	if _, err := s.accounts.Get(ctx, accountID); err != nil {
		return nil, mapAccountLookupError(err)
	}
	if s.models == nil {
		return nil, fmt.Errorf("模型仓储未初始化")
	}
	routes, err := s.models.ListSupportedForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(routes) == 0 && s.modelSyncer != nil {
		if _, syncErr := s.modelSyncer.SyncAccount(ctx, accountID); syncErr != nil {
			s.logger.Warn("account_model_probe_sync_failed", "account_id", accountID, "error", syncErr)
		} else {
			routes, err = s.models.ListSupportedForAccount(ctx, accountID)
			if err != nil {
				return nil, err
			}
		}
	}
	return compactAccountTestModels(routes), nil
}

// TestAccountModel 钉死指定账号探测一条模型路由。
func (s *Service) TestAccountModel(ctx context.Context, accountID uint64, publicID string, capability modeldomain.Capability, prompt string) (AccountModelProbeResult, error) {
	result := AccountModelProbeResult{PublicID: strings.TrimSpace(publicID), Capability: capability, Outcome: ModelProbeOutcomeError}
	if accountID == 0 {
		return result, invalidInput("账号 ID 无效")
	}
	if !isTestableCapability(capability) {
		return result, invalidInput("仅支持测试文本、图片或视频模型")
	}
	value, err := s.accounts.Get(ctx, accountID)
	if err != nil {
		return result, mapAccountLookupError(err)
	}
	if s.models == nil {
		return result, fmt.Errorf("模型仓储未初始化")
	}
	if s.providers == nil {
		return result, fmt.Errorf("Provider 注册表未初始化")
	}
	routes, err := s.models.ListSupportedForAccount(ctx, accountID)
	if err != nil {
		return result, err
	}
	item, ok := findAccountTestModel(compactAccountTestModels(routes), publicID, capability)
	if !ok {
		return result, invalidInput("该账号没有匹配的可测模型")
	}
	route := modeldomain.Route{
		PublicID:      item.PublicID,
		Provider:      value.Provider,
		UpstreamModel: item.UpstreamModel,
		Capability:    item.Capability,
	}
	result.PublicID = item.PublicID
	result.Capability = item.Capability

	credential, err := s.EnsureCredential(ctx, value, false)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	billing, err := s.loadDetectBilling(ctx, credential.ID)
	if err != nil {
		return result, err
	}

	switch route.Capability {
	case modeldomain.CapabilityResponses, modeldomain.CapabilityChat:
		return s.probeAccountText(ctx, credential, billing, route)
	case modeldomain.CapabilityImage:
		// 图片只验证能否出图，不参与风控判定。
		return withoutModelProbeRisk(s.probeAccountImage(ctx, credential, route, firstNonEmpty(strings.TrimSpace(prompt), ModelProbeDefaultMediaPrompt)))
	case modeldomain.CapabilityVideo:
		return s.probeAccountVideo(ctx, credential, billing, route, firstNonEmpty(strings.TrimSpace(prompt), ModelProbeDefaultMediaPrompt))
	default:
		return result, invalidInput("仅支持测试文本、图片或视频模型")
	}
}

func (s *Service) probeAccountText(ctx context.Context, credential accountdomain.Credential, billing *accountdomain.Billing, route modeldomain.Route) (AccountModelProbeResult, error) {
	result := AccountModelProbeResult{
		PublicID:   modeldomain.ExternalPublicID(route.Provider, route.PublicID),
		Capability: route.Capability,
		Outcome:    ModelProbeOutcomeError,
	}
	adapter, ok := s.providers.Responses(credential.Provider)
	if !ok {
		result.Error = fmt.Sprintf("Provider %s 未注册对话能力", credential.Provider)
		return result, nil
	}
	body, err := json.Marshal(map[string]any{
		"model":  route.UpstreamModel,
		"input":  modelProbeTextPrompt,
		"stream": false,
		"response_format": map[string]any{
			"type": "json_object",
		},
	})
	if err != nil {
		return result, err
	}
	response, err := adapter.ForwardResponse(ctx, provider.ResponseResourceRequest{
		Credential:    credential,
		Billing:       billing,
		Method:        http.MethodPost,
		Path:          "/responses",
		Model:         route.UpstreamModel,
		Body:          body,
		NormalizeBody: true,
		Streaming:     false,
	})
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	raw := readModelProbeBody(response)
	if response != nil && response.StatusCode >= 400 {
		result.Error = firstNonEmpty(extractProviderErrorMessage(raw), fmt.Sprintf("上游返回 HTTP %d", response.StatusCode))
		return result, nil
	}
	text := extractModelProbeText(raw)
	result.Text = text
	switch classifyModelProbeText(text) {
	case ModelProbeOutcomeOK:
		result.Outcome = ModelProbeOutcomeOK
	case ModelProbeOutcomeFlagged:
		result.Outcome = ModelProbeOutcomeFlagged
	default:
		result.Outcome = ModelProbeOutcomeError
		result.Error = "模型未返回内容"
	}
	return result, nil
}

func (s *Service) probeAccountImage(ctx context.Context, credential accountdomain.Credential, route modeldomain.Route, prompt string) (AccountModelProbeResult, error) {
	result := AccountModelProbeResult{
		PublicID:   modeldomain.ExternalPublicID(route.Provider, route.PublicID),
		Capability: route.Capability,
		Outcome:    ModelProbeOutcomeError,
	}
	adapter, ok := s.providers.ImageGeneration(credential.Provider)
	if !ok {
		result.Error = fmt.Sprintf("Provider %s 未注册图片生成能力", credential.Provider)
		return result, nil
	}
	response, err := adapter.GenerateImage(ctx, provider.ImageGenerationRequest{
		Credential:     credential,
		Model:          route.UpstreamModel,
		Prompt:         prompt,
		Count:          1,
		AspectRatio:    "1:1",
		Resolution:     "1k",
		ResponseFormat: "url",
	})
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	raw := readModelProbeBody(response)
	if response != nil && response.StatusCode >= 400 {
		result.Error = firstNonEmpty(extractProviderErrorMessage(raw), fmt.Sprintf("上游返回 HTTP %d", response.StatusCode))
		return result, nil
	}
	preview := parseImagePreviewURL(raw)
	if preview == "" {
		result.Error = "图片响应未包含可预览地址"
		return result, nil
	}
	result.Outcome = ModelProbeOutcomeOK
	result.PreviewURL = preview
	return result, nil
}

func (s *Service) probeAccountVideo(ctx context.Context, credential accountdomain.Credential, billing *accountdomain.Billing, route modeldomain.Route, prompt string) (AccountModelProbeResult, error) {
	result := AccountModelProbeResult{
		PublicID:   modeldomain.ExternalPublicID(route.Provider, route.PublicID),
		Capability: route.Capability,
		Outcome:    ModelProbeOutcomeError,
	}
	adapter, ok := s.providers.Videos(credential.Provider)
	if !ok {
		result.Error = fmt.Sprintf("Provider %s 未注册视频生成能力", credential.Provider)
		return result, nil
	}
	const probeResolution = "480p"
	started := time.Now()
	generated, err := adapter.GenerateVideo(ctx, provider.VideoRequest{
		Credential:  credential,
		Billing:     billing,
		Model:       route.UpstreamModel,
		Prompt:      prompt,
		Duration:    6,
		AspectRatio: "16:9",
		Resolution:  probeResolution,
	})
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	s.consumeProbeVideoQuota(ctx, credential, route, probeResolution)
	if provider.IsFastRemoteVideoRisk(time.Since(started), generated) {
		result.Outcome = ModelProbeOutcomeFlagged
		result.Error = provider.ErrVideoRiskScenery.Error()
		if markErr := s.MarkAccountRisk(ctx, credential); markErr != nil {
			s.logger.Error("account_model_probe_risk_write_failed", "account_id", credential.ID, "error", markErr)
			result.Error = markErr.Error()
		}
		return result, nil
	}
	preview := ""
	if assetID := strings.TrimSpace(generated.AssetID); assetID != "" {
		preview = "/v1/media/videos/" + assetID
	} else {
		preview = strings.TrimSpace(generated.URL)
	}
	if preview == "" {
		result.Error = "视频响应未包含可预览地址"
		return result, nil
	}
	result.Outcome = ModelProbeOutcomeOK
	result.PreviewURL = preview
	return result, nil
}

// MarkAccountRisk writes risk=1 from a 10s scenery video and copies the
// snapshot onto linked Build/Console credentials. Historical risk-ever is sticky.
func (s *Service) MarkAccountRisk(ctx context.Context, value accountdomain.Credential) error {
	indexed, ok := s.accounts.(buildBotFlagIndexRepository)
	if !ok {
		return fmt.Errorf("账号仓储未实现风控写入")
	}
	latest, err := s.accounts.Get(ctx, value.ID)
	if err == nil {
		value = latest
	}
	if value.BlocksBySSORisk() && value.SSOBotRiskEver {
		return nil
	}
	source := accountdomain.PersistSSOInspectSource(value.SSOBotFlagSource)
	if source == 0 {
		source = 1
	}
	update := repository.SSOBotFlagSourceUpdate{
		AccountID: value.ID, ExpectedEncryptedAccessToken: value.EncryptedAccessToken, Source: source,
		Policy: value.SSOBotPolicy, Event: value.SSOBotEvent, Risk: 1, RiskSet: true,
		Details: firstNonEmpty(value.SSOBotDetails, "video_scenery"), Inspected: true,
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialStateWriteTimeout)
	defer cancel()
	if err := indexed.UpdateSSOBotFlagSources(writeCtx, []repository.SSOBotFlagSourceUpdate{update}); err != nil {
		return err
	}
	return s.syncSSOInspectToLinkedAccounts(writeCtx, indexed, value, update)
}

func (s *Service) consumeProbeVideoQuota(ctx context.Context, credential accountdomain.Credential, route modeldomain.Route, resolution string) {
	mode := accountdomain.QuotaModeWebVideo
	if credential.Provider == accountdomain.ProviderWeb {
		mode = accountdomain.QuotaModeWebVideo720p
	}
	if s.providers != nil {
		if catalog := s.providers.QuotaMode(credential.Provider, route.UpstreamModel); catalog != "" {
			mode = probeVideoQuotaMode(credential.Provider, catalog, resolution)
		}
	}
	if mode == "" || mode == "weekly" {
		return
	}
	if _, err := s.DecrementQuota(ctx, credential.ID, mode, 1); err != nil {
		s.logger.Warn("account_model_probe_quota_decrement_failed", "account_id", credential.ID, "mode", mode, "error", err)
	}
	if credential.Provider == accountdomain.ProviderWeb {
		s.QueueQuotaRefresh(credential.ID, accountdomain.QuotaGroupWebImagine)
	}
}

func probeVideoQuotaMode(providerValue accountdomain.Provider, catalogMode, resolution string) string {
	if providerValue == accountdomain.ProviderWeb && catalogMode == accountdomain.QuotaModeWebVideo {
		resolution = strings.ToLower(strings.TrimSpace(resolution))
		if resolution == "" || resolution == "480p" || resolution == "720p" {
			return accountdomain.QuotaModeWebVideo720p
		}
	}
	return catalogMode
}

func withoutModelProbeRisk(result AccountModelProbeResult, err error) (AccountModelProbeResult, error) {
	if result.Outcome == ModelProbeOutcomeFlagged {
		result.Outcome = ModelProbeOutcomeError
		if result.Error == "" {
			result.Error = "媒体测试不参与风控判定"
		}
	}
	return result, err
}

func compactAccountTestModels(routes []modeldomain.Route) []AccountModelProbeItem {
	items := make([]AccountModelProbeItem, 0, len(routes))
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if !isTestableCapability(route.Capability) {
			continue
		}
		publicID := modeldomain.ExternalPublicID(route.Provider, route.PublicID)
		key := publicID + "\x00" + string(route.Capability)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, AccountModelProbeItem{
			PublicID:      publicID,
			UpstreamModel: route.UpstreamModel,
			Capability:    route.Capability,
		})
	}
	return items
}

func findAccountTestModel(items []AccountModelProbeItem, publicID string, capability modeldomain.Capability) (AccountModelProbeItem, bool) {
	wanted := strings.TrimSpace(publicID)
	for _, item := range items {
		if item.Capability != capability {
			continue
		}
		if accountTestPublicIDMatches(item.PublicID, wanted) {
			return item, true
		}
	}
	return AccountModelProbeItem{}, false
}

func accountTestPublicIDMatches(itemPublicID, requested string) bool {
	if strings.EqualFold(itemPublicID, requested) {
		return true
	}
	for _, providerValue := range accountdomain.Providers() {
		if strings.EqualFold(modeldomain.ExternalPublicID(providerValue, requested), itemPublicID) {
			return true
		}
	}
	return false
}

func isTestableCapability(value modeldomain.Capability) bool {
	switch value {
	case modeldomain.CapabilityResponses, modeldomain.CapabilityChat, modeldomain.CapabilityImage, modeldomain.CapabilityVideo:
		return true
	default:
		return false
	}
}

func mapAccountLookupError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func readModelProbeBody(response *provider.Response) []byte {
	if response == nil || response.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, modelProbeMaxBodyBytes+1))
	if err != nil {
		return nil
	}
	if len(data) > modelProbeMaxBodyBytes {
		return data[:modelProbeMaxBodyBytes]
	}
	return data
}

func extractModelProbeText(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return strings.TrimSpace(string(trimmed))
	}
	if text := firstNonEmpty(
		asTrimmedString(payload["output_text"]),
		extractChoicesContent(payload["choices"]),
		extractResponsesOutput(payload["output"]),
	); text != "" {
		return text
	}
	return ""
}

func classifyModelProbeText(text string) ModelProbeOutcome {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ModelProbeOutcomeError
	}
	if _, ok := parseJSONObject(stripMarkdownFence(trimmed)); ok {
		return ModelProbeOutcomeOK
	}
	return ModelProbeOutcomeFlagged
}

func parseJSONObject(text string) (map[string]any, bool) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) == nil && len(bytes.TrimSpace(extra)) > 0 {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, false
	}
	return object, true
}

func stripMarkdownFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
		lang := strings.TrimSpace(trimmed[:newline])
		if lang == "" || strings.EqualFold(lang, "json") {
			trimmed = trimmed[newline+1:]
		}
	}
	if index := strings.LastIndex(trimmed, "```"); index >= 0 {
		trimmed = trimmed[:index]
	}
	return strings.TrimSpace(trimmed)
}

func extractChoicesContent(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	var builder strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if message, ok := row["message"].(map[string]any); ok {
			if text := asTrimmedString(message["content"]); text != "" {
				builder.WriteString(text)
			}
		}
		if text := asTrimmedString(row["text"]); text != "" {
			builder.WriteString(text)
		}
	}
	return strings.TrimSpace(builder.String())
}

func extractResponsesOutput(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	var builder strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text := asTrimmedString(row["text"]); text != "" {
			builder.WriteString(text)
			continue
		}
		content, ok := row["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range content {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text := firstNonEmpty(asTrimmedString(block["text"]), asTrimmedString(block["output_text"])); text != "" {
				builder.WriteString(text)
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func parseImagePreviewURL(body []byte) string {
	var payload struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
			MIME    string `json:"mime_type"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Data) == 0 {
		return ""
	}
	item := payload.Data[0]
	if url := strings.TrimSpace(item.URL); url != "" {
		return url
	}
	if encoded := strings.TrimSpace(item.B64JSON); encoded != "" {
		mime := strings.TrimSpace(item.MIME)
		if mime == "" {
			mime = "image/png"
		}
		return "data:" + mime + ";base64," + encoded
	}
	return ""
}

func extractProviderErrorMessage(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return strings.TrimSpace(string(bytes.TrimSpace(body)))
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		return firstNonEmpty(asTrimmedString(errObj["message"]), asTrimmedString(errObj["code"]))
	}
	return firstNonEmpty(asTrimmedString(payload["message"]), asTrimmedString(payload["error"]))
}

func asTrimmedString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
