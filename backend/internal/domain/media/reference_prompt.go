package media

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	officialRefTagPattern = regexp.MustCompile(`(?i)<(image|audio)_(\d+)>`)
	webUUIDPattern        = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`
	webUUIDMentionPattern = regexp.MustCompile(`(?i)@` + webUUIDPattern)
)

// OfficialRefTag returns the canonical Build/xAI prompt tag, such as <IMAGE_0>.
func OfficialRefTag(kind string, index int) string {
	return fmt.Sprintf("<%s_%d>", strings.ToUpper(strings.TrimSpace(kind)), index)
}

// NormalizeOfficialPromptTags rewrites <image_0>/<audio_1> to canonical <IMAGE_0>/<AUDIO_1>.
func NormalizeOfficialPromptTags(prompt string) string {
	return officialRefTagPattern.ReplaceAllStringFunc(prompt, func(match string) string {
		parts := officialRefTagPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return OfficialRefTag(parts[1], atoiOrZero(parts[2]))
	})
}

// RewriteOfficialPromptToWebUUIDs converts Build-standard <IMAGE_n>/<AUDIO_n>
// tags (any case) into grok.com @uuid mentions. Tags that are not present are left out.
func RewriteOfficialPromptToWebUUIDs(prompt string, imageIDs, audioIDs []string) string {
	prompt = strings.TrimSpace(prompt)
	out := officialRefTagPattern.ReplaceAllStringFunc(prompt, func(match string) string {
		parts := officialRefTagPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		index := atoiOrZero(parts[2])
		id := ""
		switch strings.ToLower(parts[1]) {
		case "image":
			if index >= 0 && index < len(imageIDs) {
				id = strings.TrimSpace(imageIDs[index])
			}
		case "audio":
			if index >= 0 && index < len(audioIDs) {
				id = strings.TrimSpace(audioIDs[index])
			}
		}
		if id == "" {
			return match
		}
		return "@" + id
	})
	return padWebUUIDMentions(out)
}

func padWebUUIDMentions(value string) string {
	if value == "" {
		return value
	}
	var builder strings.Builder
	last := 0
	for _, loc := range webUUIDMentionPattern.FindAllStringIndex(value, -1) {
		builder.WriteString(value[last:loc[0]])
		if builder.Len() > 0 {
			prev, _ := utf8.DecodeLastRuneInString(builder.String())
			if prev != utf8.RuneError && mentionBoundary(prev) {
				builder.WriteByte(' ')
			}
		}
		builder.WriteString(value[loc[0]:loc[1]])
		if loc[1] >= len(value) {
			last = loc[1]
			continue
		}
		next, _ := utf8.DecodeRuneInString(value[loc[1]:])
		if next != utf8.RuneError && mentionBoundary(next) {
			builder.WriteByte(' ')
		}
		last = loc[1]
	}
	builder.WriteString(value[last:])
	return strings.TrimSpace(builder.String())
}

func mentionBoundary(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func atoiOrZero(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
