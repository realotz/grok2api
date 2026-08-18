package browserheaders

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var (
	chromiumVersionPattern = regexp.MustCompile(`(?i)\b(?:chrome|chromium|crios)/(\d{2,3})(?:\.\d+)*`)
	edgeVersionPattern     = regexp.MustCompile(`(?i)\b(?:edg|edga|edgios)/(\d{2,3})(?:\.\d+)*`)
)

// ApplyChromiumLowEntropyHints 只补齐浏览器默认会发送的低熵 Client Hints。
// 不主动带 Arch/Bitness/Model，那些只有站点通过 Accept-CH 索取时才会出现。
func ApplyChromiumLowEntropyHints(header http.Header, userAgent string) {
	identity, ok := parseChromiumIdentity(userAgent)
	if !ok {
		return
	}
	if identity.brand == "Google Chrome" || identity.brand == "Microsoft Edge" {
		header.Set("Sec-Ch-Ua", fmt.Sprintf(`"%s";v="%s", "Chromium";v="%s", "Not(A:Brand";v="24"`, identity.brand, identity.version, identity.version))
	} else {
		header.Set("Sec-Ch-Ua", fmt.Sprintf(`"%s";v="%s", "Not(A:Brand";v="24"`, identity.brand, identity.version))
	}
	header.Set("Sec-Ch-Ua-Mobile", identity.mobile)
	if identity.platform != "" {
		header.Set("Sec-Ch-Ua-Platform", strconv.Quote(identity.platform))
	}
}

// ApplyChromiumClientHints 根据真实 User-Agent 补齐一致的 Chromium Client Hints。
// 非 Chromium UA 不生成提示头，避免互相矛盾的浏览器指纹。
func ApplyChromiumClientHints(header http.Header, userAgent string) {
	if _, ok := parseChromiumIdentity(userAgent); !ok {
		return
	}
	ApplyChromiumLowEntropyHints(header, userAgent)
	header.Set("Sec-Ch-Ua-Model", "")

	lower := strings.ToLower(userAgent)
	arch := ""
	switch {
	case strings.Contains(lower, "aarch64") || strings.Contains(lower, "arm64") || strings.Contains(lower, " arm"):
		arch = "arm"
	case strings.Contains(lower, "x86_64") || strings.Contains(lower, "x64") || strings.Contains(lower, "win64") || strings.Contains(lower, "intel"):
		arch = "x86"
	}
	if arch != "" {
		header.Set("Sec-Ch-Ua-Arch", arch)
		header.Set("Sec-Ch-Ua-Bitness", "64")
	}
}

type chromiumIdentity struct {
	brand    string
	version  string
	platform string
	mobile   string
}

func parseChromiumIdentity(userAgent string) (chromiumIdentity, bool) {
	lower := strings.ToLower(userAgent)
	brand := "Google Chrome"
	match := chromiumVersionPattern.FindStringSubmatch(userAgent)
	if edge := edgeVersionPattern.FindStringSubmatch(userAgent); len(edge) == 2 {
		brand, match = "Microsoft Edge", edge
	} else if strings.Contains(lower, "chromium/") && !strings.Contains(lower, "chrome/") {
		brand = "Chromium"
	}
	if len(match) != 2 {
		return chromiumIdentity{}, false
	}
	platform := ""
	switch {
	case strings.Contains(lower, "windows"):
		platform = "Windows"
	case strings.Contains(lower, "mac os x") || strings.Contains(lower, "macintosh"):
		platform = "macOS"
	case strings.Contains(lower, "android"):
		platform = "Android"
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad"):
		platform = "iOS"
	case strings.Contains(lower, "linux"):
		platform = "Linux"
	}
	mobile := "?0"
	if strings.Contains(lower, "mobile") || platform == "Android" || platform == "iOS" {
		mobile = "?1"
	}
	return chromiumIdentity{brand: brand, version: match[1], platform: platform, mobile: mobile}, true
}
