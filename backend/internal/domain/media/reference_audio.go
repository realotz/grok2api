package media

import (
	"regexp"
	"strings"
	"unicode"
)

const maxVideoReferenceVoiceIDRunes = 128

var videoReferenceAssetUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsVideoReferenceAudioURL reports whether value is an HTTPS audio URL or audio data URI.
func IsVideoReferenceAudioURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:audio/")
}

// IsVideoReferenceVoiceID reports whether value is a Build/Console voice_id.
// Preset names such as eve and opaque ids are accepted; URLs are not.
func IsVideoReferenceVoiceID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || IsVideoReferenceAudioURL(value) {
		return false
	}
	if len([]rune(value)) > maxVideoReferenceVoiceIDRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// IsVideoReferenceAssetUUID reports whether value is already an Imagine audio asset UUID.
func IsVideoReferenceAssetUUID(value string) bool {
	return videoReferenceAssetUUIDPattern.MatchString(strings.TrimSpace(value))
}
