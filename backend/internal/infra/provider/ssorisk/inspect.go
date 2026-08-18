package ssorisk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/provider/browserheaders"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

const (
	responseBodyLimit = 2 << 20
	inspectTimeout    = 20 * time.Second
)

// Inspect reads grok.com with the SSO cookie and parses the robot-risk snapshot.
// It never exchanges or rewrites tokens.
func Inspect(ctx context.Context, baseURL string, credential account.Credential, egress *infraegress.Manager, cipher *security.Cipher) (AccountState, error) {
	if credential.AuthType != account.AuthTypeSSO || (credential.Provider != account.ProviderWeb && credential.Provider != account.ProviderConsole) {
		return AccountState{}, fmt.Errorf("仅 Grok Web 与 Console SSO 账号支持风控检测")
	}
	if egress == nil || cipher == nil {
		return AccountState{}, fmt.Errorf("SSO 风控检测依赖未初始化")
	}
	token, err := cipher.Decrypt(credential.EncryptedAccessToken)
	if err != nil {
		return AccountState{}, err
	}
	if strings.TrimSpace(token) == "" {
		return AccountState{}, provider.ErrUnauthorized
	}
	lease, err := egress.AcquireCredential(ctx, domainegress.ScopeWeb, credential)
	if err != nil {
		return AccountState{}, err
	}
	defer lease.Release()
	return InspectWithLease(ctx, baseURL, token, lease, egress)
}

// InspectWithLease uses an already selected Web egress lease so cookie, UA, and
// Clearance stay aligned with other grok.com browser requests.
func InspectWithLease(ctx context.Context, baseURL, token string, lease *infraegress.Lease, egress *infraegress.Manager) (AccountState, error) {
	if lease == nil || egress == nil {
		return AccountState{}, fmt.Errorf("SSO 风控检测租约未初始化")
	}
	if strings.TrimSpace(token) == "" {
		return AccountState{}, provider.ErrUnauthorized
	}
	requestCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	origin := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if origin == "" {
		origin = "https://grok.com"
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, origin+"/", nil)
	if err != nil {
		return AccountState{}, err
	}
	request.Header = homepageHeaders(token, origin, lease)
	response, err := lease.Do(request)
	if err != nil {
		egress.FeedbackForScope(context.WithoutCancel(ctx), domainegress.ScopeWeb, lease.NodeID, 0, err)
		return AccountState{Error: err.Error()}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, responseBodyLimit+1))
	if err != nil {
		return AccountState{StatusCode: response.StatusCode, URL: responseURL(response), Error: err.Error()}, err
	}
	state := AccountState{StatusCode: response.StatusCode, URL: responseURL(response)}
	if len(body) > responseBodyLimit {
		state.Error = "grok.com 响应超过安全上限"
		return state, fmt.Errorf("%s", state.Error)
	}
	egress.FeedbackForScope(context.WithoutCancel(ctx), domainegress.ScopeWeb, lease.NodeID, response.StatusCode, nil)
	if response.StatusCode == http.StatusUnauthorized {
		return state, provider.ErrUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		suffix := ""
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
			suffix = "（可能是 Cloudflare/出口限制）"
		}
		state.Error = fmt.Sprintf("grok.com HTTP %d%s", response.StatusCode, suffix)
		return state, fmt.Errorf("%s", state.Error)
	}
	parsed := Parse(string(body))
	state.Found = parsed.Found
	state.BotFlagSource = parsed.BotFlagSource
	state.BotFlagSet = parsed.BotFlagSet
	state.Details = parsed.Details
	state.Policy = parsed.Policy
	state.Risk = parsed.Risk
	state.RiskSet = parsed.RiskSet
	state.Event = parsed.Event
	state.Denied = parsed.Denied
	if !parsed.Found {
		state.Error = "grok.com 未发现 botFlag 字段"
	}
	return state, nil
}

func homepageHeaders(token, origin string, lease *infraegress.Lease) http.Header {
	userAgent := strings.TrimSpace(lease.UserAgent)
	if userAgent == "" {
		userAgent = infraegress.DefaultUserAgent
	}
	value := http.Header{}
	value.Set("Accept", "text/html,application/xhtml+xml")
	value.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	value.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	value.Set("Cache-Control", "no-cache")
	value.Set("Cookie", infraegress.BuildSSOCookie(token, lease.CFCookies))
	value.Set("Pragma", "no-cache")
	value.Set("Priority", "u=0, i")
	value.Set("Referer", origin+"/")
	value.Set("Sec-Fetch-Dest", "document")
	value.Set("Sec-Fetch-Mode", "navigate")
	value.Set("Sec-Fetch-Site", "same-origin")
	value.Set("Upgrade-Insecure-Requests", "1")
	value.Set("User-Agent", userAgent)
	browserheaders.ApplyChromiumClientHints(value, userAgent)
	return value
}

func responseURL(response *http.Response) string {
	if response == nil || response.Request == nil || response.Request.URL == nil {
		return ""
	}
	return response.Request.URL.String()
}
