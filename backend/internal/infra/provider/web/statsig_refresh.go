package web

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
)

const (
	statsigRefreshMinGap    = 5 * time.Minute
	statsigMaxScriptFetches = 64
)

var (
	statsigScriptSrcPattern = regexp.MustCompile(`(?:src|href)="(https://cdn\.grok\.com/_next/static/chunks/[^"]+\.js)"`)
	statsigSVGPathPattern   = regexp.MustCompile(`M 10,30 C[^\n"']+`)
	statsigRefreshMu        sync.Mutex
	statsigLastRefresh      time.Time
	statsigRefreshForce     bool
)

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

// refreshStatsigPair 不再用首页 seed/curves 现算 HEX。8 月 19 日起现算会把
// 已验证 pair 覆盖成 grok 不认的签名（公式下标与当前模块不一致）。
// 前端发版后应替换 statsig_local.go 的冻住 pair，而不是在这里现算。
func (a *Adapter) refreshStatsigPair(ctx context.Context, token string, lease *infraegress.Lease) error {
	statsigRefreshMu.Lock()
	defer statsigRefreshMu.Unlock()
	statsigRefreshForce = false
	statsigLastRefresh = time.Now()
	return nil
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
