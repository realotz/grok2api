package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

// SynthesizeSpeech 使用 Web 文字转语音协议，保留原账号出口与真实音频响应。
func (a *Adapter) SynthesizeSpeech(ctx context.Context, request provider.TTSRequest) (provider.TTSResult, error) {
	if strings.TrimSpace(request.Text) == "" || len([]rune(request.Text)) > 15000 || request.WithTimestamps {
		return provider.TTSResult{}, fmt.Errorf("Web TTS 要求 1–15000 字符，暂不支持 with_timestamps")
	}
	codec := strings.ToLower(request.OutputFormat.Codec)
	if codec == "" {
		codec = "mp3"
	}
	if codec != "mp3" && codec != "wav" && codec != "pcm" && codec != "opus" && codec != "flac" {
		return provider.TTSResult{}, fmt.Errorf("Web TTS 不支持该音频编码")
	}
	token, err := a.cipher.Decrypt(request.Credential.EncryptedAccessToken)
	if err != nil {
		return provider.TTSResult{}, err
	}
	lease, err := a.egress.AcquireCredential(ctx, domainegress.ScopeWeb, request.Credential)
	if err != nil {
		return provider.TTSResult{}, err
	}
	defer lease.Release()
	cfg := a.config()
	payload := map[string]any{"text": request.Text, "voiceId": request.VoiceID, "language": request.Language, "codec": codec}
	if request.OutputFormat.SampleRate > 0 {
		payload["sampleRate"] = request.OutputFormat.SampleRate
	}
	if request.OutputFormat.BitRate > 0 {
		payload["bitRate"] = request.OutputFormat.BitRate
	}
	if request.Speed > 0 {
		payload["speed"] = request.Speed
	}
	if request.OptimizeStreamingLatency > 0 {
		payload["optimizeStreamingLatency"] = request.OptimizeStreamingLatency
	}
	payload["textNormalization"] = request.TextNormalization
	response, err := a.postJSONWithReferer(ctx, cfg, lease, token, cfg.BaseURL+"/rest/app-chat/text-to-speech", payload, 2*time.Minute, cfg.BaseURL+"/")
	if err != nil {
		return provider.TTSResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, webMediaDiagnosticBodyLimit))
		return provider.TTSResult{}, newWebMediaUpstreamError(response.StatusCode, body, false)
	}
	result := provider.TTSResult{ContentType: "audio/mpeg"}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	for {
		var frame struct {
			Result struct {
				Data        string `json:"data"`
				ContentType string `json:"contentType"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		err = decoder.Decode(&frame)
		if err == io.EOF {
			break
		}
		if err != nil {
			return provider.TTSResult{}, fmt.Errorf("Web TTS 响应解析失败: %w", err)
		}
		if len(frame.Error) > 0 && string(frame.Error) != "null" {
			return provider.TTSResult{}, fmt.Errorf("Web TTS 上游拒绝合成")
		}
		if frame.Result.Data == "" {
			continue
		}
		audio, decodeErr := base64.StdEncoding.DecodeString(frame.Result.Data)
		if decodeErr != nil {
			return provider.TTSResult{}, fmt.Errorf("Web TTS 音频编码无效")
		}
		result.Audio = append(result.Audio, audio...)
		if frame.Result.ContentType != "" {
			result.ContentType = frame.Result.ContentType
		}
	}
	if len(result.Audio) == 0 {
		return provider.TTSResult{}, fmt.Errorf("Web TTS 未返回音频")
	}
	return result, nil
}

// ListTTSVoices 读取 Web 当前真实音色列表，不使用本地猜测的 voice_id。
func (a *Adapter) ListTTSVoices(ctx context.Context, credential account.Credential) ([]provider.VoiceInfo, error) {
	token, err := a.cipher.Decrypt(credential.EncryptedAccessToken)
	if err != nil {
		return nil, err
	}
	lease, err := a.egress.AcquireCredential(ctx, domainegress.ScopeWeb, credential)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg := a.config()
	req, err := http.NewRequestWithContext(call, http.MethodGet, cfg.BaseURL+"/rest/app-chat/tts-voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header = buildHeaders(token, lease, "application/json")
	applyAppHeaders(req.Header, cfg.BaseURL, cfg.BaseURL+"/")
	a.applySignedStatsig(call, req, token, lease)
	response, err := lease.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, webMediaDiagnosticBodyLimit))
		return nil, newWebMediaUpstreamError(response.StatusCode, body, false)
	}
	var payload struct {
		Voices []struct {
			ID       string `json:"voiceId"`
			Name     string `json:"name"`
			Language string `json:"language"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	result := make([]provider.VoiceInfo, 0, len(payload.Voices))
	for _, voice := range payload.Voices {
		if voice.ID != "" {
			result = append(result, provider.VoiceInfo{VoiceID: voice.ID, Name: voice.Name, Language: voice.Language})
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("Web 音色目录为空")
	}
	return result, nil
}

// GetTTSVoice 从真实目录查找指定音色。
func (a *Adapter) GetTTSVoice(ctx context.Context, credential account.Credential, voiceID string) (provider.VoiceInfo, error) {
	voices, err := a.ListTTSVoices(ctx, credential)
	if err != nil {
		return provider.VoiceInfo{}, err
	}
	for _, voice := range voices {
		if strings.EqualFold(voice.VoiceID, voiceID) {
			return voice, nil
		}
	}
	return provider.VoiceInfo{}, fmt.Errorf("未知 Web voice_id")
}
