package media

import "testing"

func TestCuratedVoiceCatalogLookup(t *testing.T) {
	catalog := CuratedVoiceCatalog{
		BaseURL: "https://app-media.x.ai/voice-samples/imagine/",
		Version: 2,
		Voices: []CuratedVoice{
			{ID: "eve", AssetID: "4c671160-f2a1-45bc-bb26-15aa8eae2357"},
			{ID: "rex", AssetID: "1ec1c4ec-0c17-415e-b979-e4e4d483f41f"},
			{ID: "skip", AssetID: ""},
		},
	}
	assetID, ok := catalog.Lookup("Eve")
	if !ok || assetID != "4c671160-f2a1-45bc-bb26-15aa8eae2357" {
		t.Fatalf("Lookup(Eve) = %q %v", assetID, ok)
	}
	if _, ok := catalog.Lookup("ara"); ok {
		t.Fatal("missing voice was found")
	}
	if _, ok := catalog.Lookup("skip"); ok {
		t.Fatal("voice without assetId was found")
	}
	if catalog.Empty() {
		t.Fatal("non-empty catalog reported empty")
	}
	if !(CuratedVoiceCatalog{}).Empty() {
		t.Fatal("empty catalog reported usable")
	}
}
