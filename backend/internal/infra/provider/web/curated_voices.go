package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mediadomain "github.com/chenyme/grok2api/backend/internal/domain/media"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"golang.org/x/net/html"
	"golang.org/x/sync/singleflight"
)

const (
	curatedVoiceCacheTTL       = time.Hour
	curatedVoiceHTMLBodyLimit  = 4 << 20
	curatedVoiceScriptID       = "server-client-data-experimentation"
	defaultCuratedVoiceBaseURL = "https://app-media.x.ai/voice-samples/imagine/"
	defaultCuratedVoiceVersion = 2
)

type curatedVoiceCache struct {
	mu        sync.Mutex
	catalog   mediadomain.CuratedVoiceCatalog
	fetchedAt time.Time
	inflight  singleflight.Group
	now       func() time.Time
}

func newCuratedVoiceCache() *curatedVoiceCache {
	return &curatedVoiceCache{now: time.Now}
}

func (c *curatedVoiceCache) clock() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *curatedVoiceCache) get(fetch func() (mediadomain.CuratedVoiceCatalog, error)) (mediadomain.CuratedVoiceCatalog, error) {
	if c == nil {
		return mediadomain.CuratedVoiceCatalog{}, fmt.Errorf("Imagine 预置音色缓存未初始化")
	}
	now := c.clock()
	c.mu.Lock()
	if !c.catalog.Empty() && now.Sub(c.fetchedAt) < curatedVoiceCacheTTL {
		catalog := c.catalog
		c.mu.Unlock()
		return catalog, nil
	}
	stale := c.catalog
	c.mu.Unlock()

	value, err, _ := c.inflight.Do("imagine_curated_voices", func() (any, error) {
		c.mu.Lock()
		if !c.catalog.Empty() && c.clock().Sub(c.fetchedAt) < curatedVoiceCacheTTL {
			catalog := c.catalog
			c.mu.Unlock()
			return catalog, nil
		}
		held := c.catalog
		c.mu.Unlock()

		catalog, err := fetch()
		if err != nil {
			if !held.Empty() {
				return held, nil
			}
			return mediadomain.CuratedVoiceCatalog{}, err
		}
		if catalog.Empty() {
			if !held.Empty() {
				return held, nil
			}
			return mediadomain.CuratedVoiceCatalog{}, fmt.Errorf("Imagine 预置音色目录为空")
		}
		c.mu.Lock()
		c.catalog = catalog
		c.fetchedAt = c.clock()
		c.mu.Unlock()
		return catalog, nil
	})
	if err != nil {
		if !stale.Empty() {
			return stale, nil
		}
		return mediadomain.CuratedVoiceCatalog{}, err
	}
	catalog, _ := value.(mediadomain.CuratedVoiceCatalog)
	return catalog, nil
}

func (a *Adapter) resolveCuratedVoiceAssetID(ctx context.Context, cfg Config, lease *infraegress.Lease, token, voiceID string) (string, error) {
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		return "", fmt.Errorf("voice_id 不能为空")
	}
	if mediadomain.IsVideoReferenceAssetUUID(voiceID) {
		return voiceID, nil
	}
	catalog, err := a.curatedVoices.get(func() (mediadomain.CuratedVoiceCatalog, error) {
		return a.fetchCuratedVoiceCatalog(ctx, cfg, lease, token)
	})
	if err != nil {
		a.log().Warn("web_curated_voices_lookup_failed", "voice_id", voiceID, "error", err)
	} else if assetID, ok := catalog.Lookup(voiceID); ok {
		return assetID, nil
	} else if !catalog.Empty() {
		return "", fmt.Errorf("未知的 voice_id %q", voiceID)
	}
	preview := curatedVoicePreviewURL(catalog, voiceID)
	if preview == "" {
		return "", fmt.Errorf("未知的 voice_id %q", voiceID)
	}
	return a.prepareVideoReferenceAudio(ctx, cfg, lease, token, preview)
}

func curatedVoicePreviewURL(catalog mediadomain.CuratedVoiceCatalog, voiceID string) string {
	voiceID = strings.ToLower(strings.TrimSpace(voiceID))
	if voiceID == "" || mediadomain.IsVideoReferenceAssetUUID(voiceID) || mediadomain.IsVideoReferenceAudioURL(voiceID) {
		return ""
	}
	base := strings.TrimSpace(catalog.BaseURL)
	if base == "" {
		base = defaultCuratedVoiceBaseURL
	}
	version := catalog.Version
	if version <= 0 {
		version = defaultCuratedVoiceVersion
	}
	return strings.TrimRight(base, "/") + "/" + voiceID + ".mp3?v=" + strconv.Itoa(version)
}

func (a *Adapter) warmCuratedVoices(ctx context.Context, cfg Config, lease *infraegress.Lease, token string) {
	if a == nil || a.curatedVoices == nil {
		return
	}
	if _, err := a.curatedVoices.get(func() (mediadomain.CuratedVoiceCatalog, error) {
		return a.fetchCuratedVoiceCatalog(ctx, cfg, lease, token)
	}); err != nil {
		a.log().Warn("web_curated_voices_warmup_failed", "error", err)
	}
}

func (a *Adapter) fetchCuratedVoiceCatalog(ctx context.Context, cfg Config, lease *infraegress.Lease, token string) (mediadomain.CuratedVoiceCatalog, error) {
	if lease == nil {
		return mediadomain.CuratedVoiceCatalog{}, fmt.Errorf("获取 Imagine 预置音色缺少出口租约")
	}
	requestCtx, cancel := context.WithTimeout(infraegress.WithPhysicalCallStage(ctx, "curated_voices"), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+"/imagine", nil)
	if err != nil {
		return mediadomain.CuratedVoiceCatalog{}, err
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("Pragma", "no-cache")
	request.Header.Set("Sec-Fetch-Dest", "document")
	request.Header.Set("Sec-Fetch-Mode", "navigate")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Upgrade-Insecure-Requests", "1")
	request.Header.Set("User-Agent", lease.UserAgent)
	request.Header.Set("Cookie", infraegress.BuildSSOCookie(token, lease.CFCookies))
	a.applySignedStatsig(requestCtx, request, token, lease)
	response, err := lease.Do(request)
	if err != nil {
		return mediadomain.CuratedVoiceCatalog{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return mediadomain.CuratedVoiceCatalog{}, fmt.Errorf("Imagine 页面返回 %d", response.StatusCode)
	}
	body, err := readMaybeGzipBody(response, curatedVoiceHTMLBodyLimit)
	if err != nil {
		return mediadomain.CuratedVoiceCatalog{}, err
	}
	raw, err := extractExperimentationScript(body)
	if err != nil {
		return mediadomain.CuratedVoiceCatalog{}, err
	}
	return parseCuratedVoiceCatalogJSON(raw)
}

func readMaybeGzipBody(response *http.Response, limit int64) ([]byte, error) {
	reader := io.Reader(response.Body)
	if strings.EqualFold(strings.TrimSpace(response.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(response.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("Imagine 页面超过安全上限")
	}
	return body, nil
}

func extractExperimentationScript(body []byte) ([]byte, error) {
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	inTarget := false
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if tokenizer.Err() == io.EOF {
				return nil, fmt.Errorf("Imagine 页面缺少预置音色配置")
			}
			return nil, tokenizer.Err()
		case html.StartTagToken:
			name, hasAttrs := tokenizer.TagName()
			if !strings.EqualFold(string(name), "script") {
				continue
			}
			inTarget = false
			for hasAttrs {
				key, value, more := tokenizer.TagAttr()
				if strings.EqualFold(string(key), "id") && string(value) == curatedVoiceScriptID {
					inTarget = true
				}
				if !more {
					break
				}
			}
		case html.TextToken:
			if inTarget {
				text := bytes.TrimSpace(tokenizer.Text())
				if len(text) == 0 {
					continue
				}
				return append([]byte(nil), text...), nil
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if strings.EqualFold(string(name), "script") {
				inTarget = false
			}
		}
	}
}

type curatedVoicesWire struct {
	BaseURL string `json:"baseUrl"`
	Version int    `json:"version"`
	Voices  []struct {
		ID          string   `json:"id"`
		AssetID     string   `json:"assetId"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	} `json:"voices"`
}

func parseCuratedVoiceCatalogJSON(raw []byte) (mediadomain.CuratedVoiceCatalog, error) {
	raw = bytes.TrimSpace(raw)
	var payload struct {
		ServerConfig map[string]json.RawMessage `json:"serverConfig"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		if inner, ok := payload.ServerConfig["imagine_curated_voices"]; ok && len(bytes.TrimSpace(inner)) > 0 {
			return decodeCuratedVoicesWire(inner)
		}
	}
	return decodeCuratedVoicesWire(raw)
}

func decodeCuratedVoicesWire(raw []byte) (mediadomain.CuratedVoiceCatalog, error) {
	var wire curatedVoicesWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return mediadomain.CuratedVoiceCatalog{}, fmt.Errorf("解析 Imagine 预置音色: %w", err)
	}
	catalog := mediadomain.CuratedVoiceCatalog{
		BaseURL: strings.TrimSpace(wire.BaseURL),
		Version: wire.Version,
		Voices:  make([]mediadomain.CuratedVoice, 0, len(wire.Voices)),
	}
	for _, voice := range wire.Voices {
		id := strings.TrimSpace(voice.ID)
		assetID := strings.TrimSpace(voice.AssetID)
		if id == "" || assetID == "" {
			continue
		}
		catalog.Voices = append(catalog.Voices, mediadomain.CuratedVoice{
			ID:          id,
			AssetID:     assetID,
			Description: strings.TrimSpace(voice.Description),
			Tags:        append([]string(nil), voice.Tags...),
		})
	}
	return catalog, nil
}
