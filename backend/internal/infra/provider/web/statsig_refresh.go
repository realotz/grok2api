package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
)

const (
	statsigPairFileName     = "statsig-pair.json"
	statsigRefreshMinGap    = 5 * time.Minute
	statsigScriptBodyLimit  = 2 << 20
	statsigMaxScriptFetches = 64
)

var (
	statsigScriptSrcPattern = regexp.MustCompile(`(?:src|href)="(https://cdn\.grok\.com/_next/static/chunks/[^"]+\.js)"`)
	statsigSVGPathPattern   = regexp.MustCompile(`M 10,30 C[^\n"']+`)
	statsigRefreshMu        sync.Mutex
	statsigLastRefresh      time.Time
	statsigRefreshForce     bool
)

type persistedStatsigPair struct {
	Seed      string    `json:"seed"`
	HEX       string    `json:"hex"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func init() {
	// 不加载 data/statsig-pair.json。运行时现算 HEX 曾把已验证的冻住 pair
	// 覆盖成 grok.com 不认的签名，线上表现为 403 code 7。
}

func markStatsigPairStale() {
	statsigRefreshMu.Lock()
	statsigRefreshForce = true
	statsigRefreshMu.Unlock()
}

func resetStatsigRefreshState() {
	statsigRefreshMu.Lock()
	statsigRefreshForce = false
	statsigLastRefresh = time.Time{}
	statsigRefreshMu.Unlock()
}

func statsigPairNeedsRefresh() bool {
	statsigRefreshMu.Lock()
	defer statsigRefreshMu.Unlock()
	return statsigPairNeedsRefreshLocked()
}

func statsigPairNeedsRefreshLocked() bool {
	if statsigRefreshForce {
		return true
	}
	return statsigLastRefresh.IsZero() || time.Since(statsigLastRefresh) >= statsigRefreshMinGap
}

// refreshStatsigPair 不再用首页 seed/curves 现算 HEX。grok 只校验 pair 自洽，
// 冻住的浏览器抓包 pair 加新鲜时间戳即可；现算会把可用签名覆盖成 403 code 7。
func (a *Adapter) refreshStatsigPair(ctx context.Context, token string, lease *infraegress.Lease) error {
	statsigRefreshMu.Lock()
	defer statsigRefreshMu.Unlock()
	statsigRefreshForce = false
	statsigLastRefresh = time.Now()
	return nil
}

func fetchStatsigPage(ctx context.Context, baseURL, token string, lease *infraegress.Lease, do func(*http.Request) (*http.Response, error)) ([]byte, error) {
	root, err := fetchStatsigMetaResponse(ctx, baseURL, token, lease, "/", do)
	if err != nil {
		return nil, err
	}
	if root.statusCode >= 200 && root.statusCode < 300 {
		return root.body, nil
	}
	if root.statusCode != http.StatusNotFound {
		return nil, statsigMetaStatusError("/", root.statusCode)
	}
	index, err := fetchStatsigMetaResponse(ctx, baseURL, token, lease, "/index", do)
	if err != nil {
		return nil, err
	}
	if index.statusCode < 200 || index.statusCode >= 300 {
		return nil, statsigMetaStatusError("/index", index.statusCode)
	}
	return index.body, nil
}

func collectStatsigSVGPaths(html string, fetchBody func(string) (string, bool)) []string {
	found := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(paths []string) {
		for _, path := range paths {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			found = append(found, path)
		}
	}
	add(extractStatsigCurvePaths(html))
	add(extractStatsigSVGPaths(html))
	urls := extractStatsigScriptURLs(html)
	if fetchBody != nil {
		if len(urls) > statsigMaxScriptFetches {
			urls = urls[:statsigMaxScriptFetches]
		}
		for _, scriptURL := range urls {
			body, ok := fetchBody(scriptURL)
			if !ok {
				continue
			}
			add(extractStatsigSVGPaths(body))
			if len(found) >= 4 {
				break
			}
		}
	}
	return found
}

func extractStatsigScriptURLs(html string) []string {
	matches := statsigScriptSrcPattern.FindAllStringSubmatch(html, -1)
	urls := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		url := match[1]
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}
	return urls
}

func extractStatsigCurvePaths(html string) []string {
	groups := extractStatsigCurveGroups(html)
	if len(groups) < 4 {
		return nil
	}
	out := make([]string, 0, 4)
	for _, group := range groups[:4] {
		path := statsigCurveGroupToPath(group)
		if !strings.HasPrefix(path, "M 10,30 C") || strings.Count(path, "C") < 4 {
			return nil
		}
		out = append(out, path)
	}
	return out
}

type statsigCurveSeg struct {
	Color  [6]int `json:"color"`
	Deg    int    `json:"deg"`
	Bezier [4]int `json:"bezier"`
}

func extractStatsigCurveGroups(html string) [][]statsigCurveSeg {
	marker := `"curves":`
	idx := strings.Index(html, marker)
	if idx < 0 {
		marker = `\"curves\":`
		idx = strings.Index(html, marker)
	}
	if idx < 0 {
		return nil
	}
	window := html[idx:]
	if len(window) > 80_000 {
		window = window[:80_000]
	}
	window = strings.ReplaceAll(window, `\"`, `"`)
	arrayStart := strings.Index(window, "[[")
	if arrayStart < 0 {
		return nil
	}
	raw, ok := sliceBalancedJSONArray(window[arrayStart:])
	if !ok {
		return nil
	}
	var groups [][]statsigCurveSeg
	if json.Unmarshal([]byte(raw), &groups) != nil || len(groups) < 4 {
		return nil
	}
	return groups
}

func sliceBalancedJSONArray(src string) (string, bool) {
	depth := 0
	for i, ch := range src {
		switch ch {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return src[:i+1], true
			}
		}
	}
	return "", false
}

func statsigCurveGroupToPath(group []statsigCurveSeg) string {
	if len(group) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("M 10,30 C")
	for i, seg := range group {
		if i > 0 {
			b.WriteString(" C")
		}
		fmt.Fprintf(&b, " %d,%d %d,%d %d,%d h %d s %d,%d %d,%d",
			seg.Color[0], seg.Color[1], seg.Color[2], seg.Color[3], seg.Color[4], seg.Color[5],
			seg.Deg, seg.Bezier[0], seg.Bezier[1], seg.Bezier[2], seg.Bezier[3])
	}
	return b.String()
}

func extractStatsigSVGPaths(source string) []string {
	matches := statsigSVGPathPattern.FindAllString(source, 8)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 80 || strings.Count(match, "C") < 4 {
			continue
		}
		out = append(out, match)
	}
	return out
}

func fetchStatsigScript(ctx context.Context, scriptURL, token string, lease *infraegress.Lease) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, scriptURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Referer", "https://grok.com/")
	if lease != nil {
		request.Header.Set("User-Agent", lease.UserAgent)
		request.Header.Set("Cookie", infraegress.BuildSSOCookie(token, lease.CFCookies))
	}
	response, err := lease.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Statsig 脚本返回 %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, statsigScriptBodyLimit))
}

func persistStatsigPair(seedB64, hex string) error {
	payload, err := json.Marshal(persistedStatsigPair{Seed: seedB64, HEX: hex, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	path := statsigPairFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadPersistedStatsigPair() (struct {
	seed []byte
	hex  string
}, error) {
	var empty struct {
		seed []byte
		hex  string
	}
	raw, err := os.ReadFile(statsigPairFilePath())
	if err != nil {
		return empty, err
	}
	var value persistedStatsigPair
	if json.Unmarshal(raw, &value) != nil {
		return empty, fmt.Errorf("Statsig 缓存无效")
	}
	seed, err := decodeStatsigSeed(value.Seed)
	if err != nil || len(seed) != 48 || strings.TrimSpace(value.HEX) == "" {
		return empty, fmt.Errorf("Statsig 缓存种子无效")
	}
	return struct {
		seed []byte
		hex  string
	}{seed: seed, hex: value.HEX}, nil
}

func statsigPairFilePath() string {
	return filepath.Join("data", statsigPairFileName)
}
