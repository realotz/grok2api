package media

import "testing"

func TestIsVideoReferenceVoiceID(t *testing.T) {
	for _, value := range []string{"eve", "Ara", "leo", "custom-voice_1", "147d593e-e005-4b21-bcde-0c0028ca0fed"} {
		if !IsVideoReferenceVoiceID(value) {
			t.Fatalf("voice_id %q rejected", value)
		}
	}
	for _, value := range []string{"", "https://example.com/a.mp3", "data:audio/mpeg;base64,SUQz", "eve voice", "eve/ara"} {
		if IsVideoReferenceVoiceID(value) {
			t.Fatalf("voice_id %q accepted", value)
		}
	}
}

func TestIsVideoReferenceAssetUUID(t *testing.T) {
	if !IsVideoReferenceAssetUUID("4c671160-f2a1-45bc-bb26-15aa8eae2357") {
		t.Fatal("asset UUID rejected")
	}
	if IsVideoReferenceAssetUUID("eve") || IsVideoReferenceAssetUUID("4c671160") {
		t.Fatal("non-UUID accepted")
	}
}

func TestIsVideoReferenceAudioURL(t *testing.T) {
	if !IsVideoReferenceAudioURL("https://example.com/a.mp3") || !IsVideoReferenceAudioURL("data:audio/mpeg;base64,SUQz") {
		t.Fatal("audio URL rejected")
	}
	if IsVideoReferenceAudioURL("eve") || IsVideoReferenceAudioURL("http://example.com/a.mp3") {
		t.Fatal("non-URL accepted")
	}
}
