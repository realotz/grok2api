package ssorisk

import (
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	userBotFlagSourceField  protowire.Number = 45
	userBotFlagDetailsField protowire.Number = 46
)

var (
	botFlagSourceRe  = regexp.MustCompile(`botFlagSource"\s*:\s*(null|-?\d+)`)
	botFlagDetailsRe = regexp.MustCompile(`botFlagDetails"\s*:\s*(?:null|"([^"]*)")`)
)

// ParseUserMessage reads prod_auth.User bot_flag_source / bot_flag_details.
// GetUser omits both fields on clean accounts; that is a successful inspect.
func ParseUserMessage(message []byte) (AccountState, error) {
	state := AccountState{Found: true}
	rest := message
	for len(rest) > 0 {
		number, fieldType, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return AccountState{}, fmt.Errorf("解析 GetUser protobuf tag 无效")
		}
		rest = rest[n:]
		switch {
		case number == userBotFlagSourceField && fieldType == protowire.VarintType:
			value, consumed := protowire.ConsumeVarint(rest)
			if consumed < 0 {
				return AccountState{}, fmt.Errorf("解析 bot_flag_source 无效")
			}
			state.BotFlagSource = int(value)
			state.BotFlagSet = true
			rest = rest[consumed:]
		case number == userBotFlagDetailsField && fieldType == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(rest)
			if consumed < 0 {
				return AccountState{}, fmt.Errorf("解析 bot_flag_details 无效")
			}
			state.Details = string(value)
			rest = rest[consumed:]
		default:
			consumed := protowire.ConsumeFieldValue(number, fieldType, rest)
			if consumed < 0 {
				return AccountState{}, fmt.Errorf("解析 GetUser protobuf 字段无效")
			}
			rest = rest[consumed:]
		}
	}
	applyDetails(&state)
	return state, nil
}

// Parse extracts grok.com homepage RSC botFlagSource / botFlagDetails.
// Kept for fixtures; live inspect uses GetUser protobuf.
func Parse(pageHTML string) AccountState {
	raw := strings.ReplaceAll(pageHTML, `\"`, `"`)
	sourceMatch := botFlagSourceRe.FindStringSubmatch(raw)
	detailsMatch := botFlagDetailsRe.FindStringSubmatch(raw)

	state := AccountState{Found: len(sourceMatch) > 0 || len(detailsMatch) > 0}
	if len(sourceMatch) > 1 && sourceMatch[1] != "null" {
		if source, ok := parseOptionalInt(sourceMatch[1]); ok {
			state.BotFlagSource = source
			state.BotFlagSet = true
		}
	}
	if len(detailsMatch) > 1 {
		state.Details = detailsMatch[1]
	}
	applyDetails(&state)
	return state
}

func applyDetails(state *AccountState) {
	fields := parseDetailFields(state.Details)
	state.Policy = strings.ToLower(fields["policy"])
	state.Event = fields["event"]
	if risk, ok := parseOptionalFloat(fields["risk"]); ok {
		state.Risk = risk
		state.RiskSet = true
	}
	state.Denied = state.Policy == "deny"
}
