package media

import "strings"

// CuratedVoice is one Imagine preset voice from grok.com experimentation config.
type CuratedVoice struct {
	ID          string
	AssetID     string
	Description string
	Tags        []string
}

// CuratedVoiceCatalog is the Imagine Audio panel mapping: Build/Console voice_id → Web asset UUID.
type CuratedVoiceCatalog struct {
	BaseURL string
	Version int
	Voices  []CuratedVoice
}

// Lookup returns the Imagine audio asset UUID for a Build-style voice_id (case-insensitive).
func (c CuratedVoiceCatalog) Lookup(voiceID string) (string, bool) {
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		return "", false
	}
	for _, voice := range c.Voices {
		if !strings.EqualFold(strings.TrimSpace(voice.ID), voiceID) {
			continue
		}
		if assetID := strings.TrimSpace(voice.AssetID); assetID != "" {
			return assetID, true
		}
	}
	return "", false
}

// Empty reports whether the catalog has no usable id → assetId entries.
func (c CuratedVoiceCatalog) Empty() bool {
	for _, voice := range c.Voices {
		if strings.TrimSpace(voice.ID) != "" && strings.TrimSpace(voice.AssetID) != "" {
			return false
		}
	}
	return true
}
