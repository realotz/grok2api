package ssorisk

import (
	"bytes"
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
	getUserPath       = "/auth_mgmt.AuthManagement/GetUser"
	responseBodyLimit = 64 << 10
	inspectTimeout    = 20 * time.Second
)

var emptyGRPCWebMessage = []byte{0, 0, 0, 0, 0}

// Inspect reads grok.com AuthManagement GetUser and parses bot_flag_source.
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
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, origin+getUserPath, bytes.NewReader(emptyGRPCWebMessage))
	if err != nil {
		return AccountState{}, err
	}
	request.Header = getUserHeaders(token, origin, lease)
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
		state.Error = "GetUser 响应超过安全上限"
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
		state.Error = fmt.Sprintf("GetUser HTTP %d%s", response.StatusCode, suffix)
		return state, fmt.Errorf("%s", state.Error)
	}
	message, grpcStatus, err := firstGRPCWebMessage(body)
	if err != nil {
		state.Error = err.Error()
		return state, err
	}
	if headerStatus := strings.TrimSpace(response.Header.Get("grpc-status")); headerStatus != "" {
		grpcStatus = headerStatus
	}
	if grpcStatus == "16" {
		return state, provider.ErrUnauthorized
	}
	if grpcStatus != "" && grpcStatus != "0" {
		state.Error = fmt.Sprintf("GetUser gRPC 状态 %s", grpcStatus)
		return state, fmt.Errorf("%s", state.Error)
	}
	if message == nil {
		state.Error = "GetUser 响应缺少消息帧"
		return state, fmt.Errorf("%s", state.Error)
	}
	parsed, err := ParseUserMessage(message)
	if err != nil {
		state.Error = err.Error()
		return state, err
	}
	state.Found = parsed.Found
	state.BotFlagSource = parsed.BotFlagSource
	state.BotFlagSet = parsed.BotFlagSet
	state.Details = parsed.Details
	state.Policy = parsed.Policy
	state.Risk = parsed.Risk
	state.RiskSet = parsed.RiskSet
	state.Event = parsed.Event
	state.Denied = parsed.Denied
	return state, nil
}

func getUserHeaders(token, origin string, lease *infraegress.Lease) http.Header {
	userAgent := strings.TrimSpace(lease.UserAgent)
	if userAgent == "" {
		userAgent = infraegress.DefaultUserAgent
	}
	value := http.Header{}
	value.Set("Accept", "*/*")
	value.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	value.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	value.Set("Cache-Control", "no-cache")
	value.Set("Content-Type", "application/grpc-web+proto")
	value.Set("Cookie", infraegress.BuildSSOCookie(token, lease.CFCookies))
	value.Set("Origin", origin)
	value.Set("Pragma", "no-cache")
	value.Set("Priority", "u=1, i")
	value.Set("Referer", origin+"/")
	value.Set("Sec-Fetch-Dest", "empty")
	value.Set("Sec-Fetch-Mode", "cors")
	value.Set("Sec-Fetch-Site", "same-origin")
	value.Set("User-Agent", userAgent)
	value.Set("x-grpc-web", "1")
	value.Set("x-user-agent", "connect-es/2.1.1")
	browserheaders.ApplyChromiumLowEntropyHints(value, userAgent)
	return value
}

func responseURL(response *http.Response) string {
	if response == nil || response.Request == nil || response.Request.URL == nil {
		return ""
	}
	return response.Request.URL.String()
}
