package ssorisk

import (
	"regexp"
	"strings"
)

var (
	botFlagSourceRe  = regexp.MustCompile(`botFlagSource"\s*:\s*(null|-?\d+)`)
	botFlagDetailsRe = regexp.MustCompile(`botFlagDetails"\s*:\s*(?:null|"([^"]*)")`)
)

// Parse extracts grok.com homepage RSC botFlagSource / botFlagDetails.
// Clean accounts typically have botFlagSource=0; 1 and 2 are robot marks.
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

	fields := parseDetailFields(state.Details)
	state.Policy = strings.ToLower(fields["policy"])
	state.Event = fields["event"]
	if risk, ok := parseOptionalFloat(fields["risk"]); ok {
		state.Risk = risk
		state.RiskSet = true
	}
	state.Denied = state.Policy == "deny"
	return state
}
