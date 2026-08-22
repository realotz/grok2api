package media

import "testing"

func TestNormalizeOfficialPromptTagsIsCaseInsensitive(t *testing.T) {
	got := NormalizeOfficialPromptTags("Use <image_0> and <AuDiO_1> please")
	want := "Use <IMAGE_0> and <AUDIO_1> please"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalizeOfficialPromptTagsDoesNotInsertMissingTags(t *testing.T) {
	got := NormalizeOfficialPromptTags("根据故事板生成视频")
	if got != "根据故事板生成视频" {
		t.Fatalf("got %q", got)
	}
}

func TestRewriteOfficialPromptToWebUUIDsConvertsTags(t *testing.T) {
	image := "e5238206-0e05-45da-8db6-9d96220b2492"
	audio := "14ca4304-64a1-4963-9f06-74f93fcf09b6"
	got := RewriteOfficialPromptToWebUUIDs("The person from <image_0> speaks with <AUDIO_0>.", []string{image}, []string{audio})
	want := "The person from @" + image + " speaks with @" + audio + "."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRewriteOfficialPromptToWebUUIDsDoesNotAppendMissingMentions(t *testing.T) {
	image := "dd6d44cd-20ae-4b44-a348-1376755072bc"
	audio := "c75a0ba4-0e2f-4a0b-b1f3-acee2d2a1887"
	got := RewriteOfficialPromptToWebUUIDs("根据故事板生成视频", []string{image}, []string{audio})
	if got != "根据故事板生成视频" {
		t.Fatalf("got %q", got)
	}
}

func TestRewriteOfficialPromptToWebUUIDsKeepsIndependentIndexOrder(t *testing.T) {
	images := []string{
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	}
	audios := []string{
		"cccccccc-cccc-cccc-cccc-cccccccccccc",
		"dddddddd-dddd-dddd-dddd-dddddddddddd",
	}
	got := RewriteOfficialPromptToWebUUIDs(
		"<IMAGE_1> then <AUDIO_0> then <image_0> then <audio_1>",
		images,
		audios,
	)
	want := "@" + images[1] + " then @" + audios[0] + " then @" + images[0] + " then @" + audios[1]
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRewriteOfficialPromptToWebUUIDsPadsMentions(t *testing.T) {
	id := "e5238206-0e05-45da-8db6-9d96220b2492"
	got := RewriteOfficialPromptToWebUUIDs("go<IMAGE_0>now", []string{id}, nil)
	want := "go @" + id + " now"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
