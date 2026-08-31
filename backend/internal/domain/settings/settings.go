package settings

import (
	"strings"
	"time"
)

const (
	DefaultBuildResponseHeaderTimeout = 5 * time.Minute
	MinBuildResponseHeaderTimeout     = 30 * time.Second
	MaxBuildResponseHeaderTimeout     = 30 * time.Minute

	DefaultBuildStreamIdleTimeout = 2 * time.Minute
	MinBuildStreamIdleTimeout     = 30 * time.Second
	MaxBuildStreamIdleTimeout     = 10 * time.Minute

	DefaultWebStreamIdleTimeout     = 90 * time.Second
	DefaultConsoleStreamIdleTimeout = 2 * time.Minute
	MinProviderStreamIdleTimeout    = 30 * time.Second
	MaxProviderStreamIdleTimeout    = 10 * time.Minute
)

// Config 表示可跨重启持久化并支持热加载的网关运行参数。
type Config struct {
	Server            ServerConfig
	ProviderBuild     ProviderBuildConfig
	ProviderWeb       ProviderWebConfig
	ProviderConsole   ProviderConsoleConfig
	Batch             BatchConfig
	Media             MediaConfig
	Frontend          FrontendConfig
	Routing           RoutingConfig
	Audit             AuditConfig
	ClientKeyDefaults ClientKeyDefaultsConfig
	Accounts          AccountsConfig
}

// ServerConfig 定义可热更新的推理入口容量参数。
type ServerConfig struct {
	MaxConcurrentRequests int
}

// FrontendConfig 定义公开 API 地址的运行时覆盖值；留空时使用配置文件值。
type FrontendConfig struct {
	PublicAPIBaseURL string
}

type ProviderConsoleConfig struct {
	BaseURL           string
	ChatTimeout       time.Duration
	StreamIdleTimeout time.Duration
}

type MediaConfig struct {
	MaxImageBytes           int64
	MaxTotalBytes           int64
	CleanupThresholdPercent int
	CleanupInterval         time.Duration
}

type ProviderWebConfig struct {
	BaseURL             string
	StatsigMode         string
	StatsigManualValue  string
	StatsigSignerURL    string
	ClearanceMode       string
	FlareSolverrURL     string
	ClearanceTimeout    time.Duration
	ClearanceRefresh    time.Duration
	QuotaTimeout        time.Duration
	ChatTimeout         time.Duration
	StreamIdleTimeout   time.Duration
	ImageTimeout        time.Duration
	VideoTimeout        time.Duration
	MediaConcurrency    int
	AllowNSFW           bool
	RecoveryBackoffBase time.Duration
	RecoveryBackoffMax  time.Duration
}

// BatchConfig 定义账号导入、转换、同步、凭据刷新和账号检测的并发上限。
type BatchConfig struct {
	ImportConcurrency     int
	ConversionConcurrency int
	SyncConcurrency       int
	RefreshConcurrency    int
	DetectConcurrency     int
	RandomDelay           *time.Duration
}

// ProviderBuildConfig 定义 Grok Build CLI 上游协议标识。
type ProviderBuildConfig struct {
	BaseURL               string
	FallbackBaseURL       string
	ClientVersion         string
	ClientIdentifier      string
	TokenAuth             string
	UserAgent             string
	ResponseHeaderTimeout time.Duration
	StreamIdleTimeout     time.Duration
}

// RoutingConfig 定义会话粘性、冷却和故障切换边界。
type RoutingConfig struct {
	StickyTTL        time.Duration
	CooldownBase     time.Duration
	CooldownMax      time.Duration
	CapacityWait     time.Duration
	MaxAttempts      int
	VideoMaxAttempts int
	PreferFreeBuild  bool
	// MarkBuildChatDeniedAsReauth 为 true 时，Build chat 权限拒绝标 reauthRequired，默认 false 保留模型级冷却。
	MarkBuildChatDeniedAsReauth bool
	// AccountIsolatedConnections is optional so persisted payloads written by
	// older releases do not silently override a value supplied by config.yaml.
	AccountIsolatedConnections *bool
	SegmentedSelector          *SegmentedSelectorConfig
}

type SegmentedSelectorConfig struct {
	ActiveEnabled bool
	MinCandidates int
	WindowSize    int
}

type AuditConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
	CommitDelay   time.Duration
	RetentionDays *int
}

// ClientKeyDefaultsConfig 定义新建客户端密钥的默认限制。
type ClientKeyDefaultsConfig struct {
	RPMLimit      int
	MaxConcurrent int
}

// AccountsConfig 定义账号池后台维护策略；默认全部关闭。
type AccountsConfig struct {
	// MarkBuildForbiddenReauth marks high-confidence Grok Build permission denials as requiring reauthorization.
	MarkBuildForbiddenReauth bool
	// BuildForbiddenReauthCodes contains exact upstream error codes that opt into account invalidation.
	BuildForbiddenReauthCodes []string
	// ExcludeBuildBotFlaggedFromScheduling 为 true 时，bot_flag_source/bfs∈{1,2} 的 Build 账号不参与调度。
	// 仅影响 ProviderBuild 选号；关联 Web/Console 账号调度不受影响。
	ExcludeBuildBotFlaggedFromScheduling bool
	// AutoCleanReauthEnabled 为 true 时，周期性删除已标记 reauthRequired 且超过 minAge 的账号。
	AutoCleanReauthEnabled bool
	// AutoCleanReauthInterval 自动清理扫描间隔。
	AutoCleanReauthInterval time.Duration
	// AutoCleanReauthMinAge 仅删除 reauth_marked_at 早于该时长的 reauthRequired 账号。
	AutoCleanReauthMinAge time.Duration
	// AutoCleanIncludeDisabled 为 true 时，reauth 清理时包含 enabled=false 的账号。
	AutoCleanIncludeDisabled bool
	// SSOVideoRiskThreshold 禁止视频及 Web Image 2.0 调度的 grok.com risk 下限；nil 表示默认 1，0 表示不限制。
	SSOVideoRiskThreshold *float64
	// SSOLLMRiskThreshold 禁止 grok-4.5/4.6 Build LLM 的 grok.com risk 下限；nil 表示默认 0.8，0 表示不限制。
	SSOLLMRiskThreshold *float64
	// SSOVideoRiskEver 控制历史风控（sso_bot_risk_ever）是否禁止正式视频及 Web Image 2.0 调度。
	// nil/空/"auto" 为默认：不调度历史风控=1；"on" 始终排除；"off" 仅按当前阈值。
	SSOVideoRiskEver *string
}

const (
	DefaultSSOVideoRiskThreshold = 1.0
	DefaultSSOLLMRiskThreshold   = 0.8

	SSOVideoRiskEverAuto    = "auto"
	SSOVideoRiskEverOn      = "on"
	SSOVideoRiskEverOff     = "off"
	DefaultSSOVideoRiskEver = SSOVideoRiskEverAuto
)

// NormalizeSSOVideoRiskEver maps empty/unknown values to auto.
func NormalizeSSOVideoRiskEver(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SSOVideoRiskEverOn:
		return SSOVideoRiskEverOn
	case SSOVideoRiskEverOff:
		return SSOVideoRiskEverOff
	default:
		return SSOVideoRiskEverAuto
	}
}

// ExcludeSSOVideoRiskEver reports whether production video should skip
// accounts with historical grok.com risk. auto and on exclude; off does not.
func ExcludeSSOVideoRiskEver(value string) bool {
	return NormalizeSSOVideoRiskEver(value) != SSOVideoRiskEverOff
}

// ValidSSOVideoRiskEver reports whether value is empty or a known mode.
func ValidSSOVideoRiskEver(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", SSOVideoRiskEverAuto, SSOVideoRiskEverOn, SSOVideoRiskEverOff:
		return true
	default:
		return false
	}
}
