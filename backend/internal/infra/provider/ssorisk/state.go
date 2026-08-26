package ssorisk

import (
	"strconv"
	"strings"
)

// AccountState is the grok.com registration/login risk snapshot for one SSO.
// It is distinct from Build JWT bot_flag_source/bfs.
type AccountState struct {
	Found         bool
	BotFlagSource int
	BotFlagSet    bool
	Details       string
	Policy        string
	Risk          float64
	RiskSet       bool
	Event         string
	Denied        bool
	StatusCode    int
	URL           string
	Error         string
}

const (
	VerdictFlagged = "flagged"
	VerdictClean   = "clean"
	VerdictError   = "error"
	VerdictUnknown = "unknown"
)

// FlagSource maps a parsed grok.com snapshot to the persisted robot mark.
// 1 = CASTLE / policy=deny, 2 = BOT_MONITOR.
func FlagSource(state AccountState) int {
	if state.BotFlagSet && (state.BotFlagSource == 1 || state.BotFlagSource == 2) {
		return state.BotFlagSource
	}
	if state.Denied || strings.EqualFold(strings.TrimSpace(state.Policy), "deny") {
		return 1
	}
	return 0
}

// Classify maps inspect output to flagged / clean / error / unknown.
func Classify(state AccountState) string {
	if state.Denied || FlagSource(state) != 0 || strings.EqualFold(strings.TrimSpace(state.Policy), "deny") {
		return VerdictFlagged
	}
	if state.Found {
		return VerdictClean
	}
	if state.StatusCode != 0 && state.StatusCode != 200 {
		return VerdictError
	}
	if strings.TrimSpace(state.Error) != "" {
		return VerdictError
	}
	return VerdictUnknown
}

func parseDetailFields(details string) map[string]string {
	fields := make(map[string]string)
	for _, item := range strings.Split(details, ",") {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields
}

func parseOptionalInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseOptionalFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
