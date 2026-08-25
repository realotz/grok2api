package web

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	mediadomain "github.com/chenyme/grok2api/backend/internal/domain/media"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

const curatedVoiceHTML = `<!doctype html><html><body>
<script type="application/json" id="server-client-data-experimentation">{"status":"ready","serverConfig":{"imagine_curated_voices":{"baseUrl":"https://app-media.x.ai/voice-samples/imagine/","version":2,"voices":[{"id":"eve","assetId":"4c671160-f2a1-45bc-bb26-15aa8eae2357","description":"Energetic and upbeat","tags":["Assistant"]},{"id":"rex","assetId":"1ec1c4ec-0c17-415e-b979-e4e4d483f41f"}]}}}</script>
</body></html>`

func TestCuratedVoicePreviewURL(t *testing.T) {
	got := curatedVoicePreviewURL(mediadomain.CuratedVoiceCatalog{}, "Rex")
	want := "https://app-media.x.ai/voice-samples/imagine/rex.mp3?v=2"
	if got != want {
		t.Fatalf("default preview = %q want %q", got, want)
	}
	got = curatedVoicePreviewURL(mediadomain.CuratedVoiceCatalog{BaseURL: "https://cdn.example/voices/", Version: 7}, "eve")
	if got != "https://cdn.example/voices/eve.mp3?v=7" {
		t.Fatalf("catalog preview = %q", got)
	}
}

func TestParseCuratedVoiceCatalogJSON(t *testing.T) {
	raw, err := extractExperimentationScript([]byte(curatedVoiceHTML))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCuratedVoiceCatalogJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.BaseURL != "https://app-media.x.ai/voice-samples/imagine/" || catalog.Version != 2 || len(catalog.Voices) != 2 {
		t.Fatalf("catalog = %#v", catalog)
	}
	assetID, ok := catalog.Lookup("EVE")
	if !ok || assetID != "4c671160-f2a1-45bc-bb26-15aa8eae2357" {
		t.Fatalf("Lookup(EVE) = %q %v", assetID, ok)
	}
}

func TestCuratedVoiceCacheTTLAndStaleKeep(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cache := newCuratedVoiceCache()
	cache.now = func() time.Time { return now }
	var fetches atomic.Int32
	live := func() (mediadomain.CuratedVoiceCatalog, error) {
		fetches.Add(1)
		return mustParseCuratedHTML(t), nil
	}
	first, err := cache.get(live)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.Lookup("eve"); !ok {
		t.Fatal("first lookup missed eve")
	}
	now = now.Add(30 * time.Minute)
	if _, err := cache.get(live); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("fresh cache fetched %d times", fetches.Load())
	}
	now = now.Add(31 * time.Minute)
	if _, err := cache.get(live); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("expired cache fetched %d times", fetches.Load())
	}
	now = now.Add(2 * time.Hour)
	kept, err := cache.get(func() (mediadomain.CuratedVoiceCatalog, error) {
		fetches.Add(1)
		return parseCuratedVoiceCatalogJSON([]byte(`{"baseUrl":"https://example.invalid/","version":3,"voices":[]}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := kept.Lookup("rex"); !ok {
		t.Fatal("empty refresh overwrote a good catalog")
	}
}

func TestPrepareVideoReferenceAudioMapsVoiceIDWithoutUpload(t *testing.T) {
	var uploads atomic.Int32
	var imagines atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/imagine":
			imagines.Add(1)
			writer.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(writer, curatedVoiceHTML)
		case "/http/upload-file-v2/direct":
			uploads.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	manager := infraegress.NewManager(egressRepositoryStub{}, cipher)
	lease, err := manager.Acquire(context.Background(), egressdomain.ScopeWeb, "curated-voice-test")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	adapter := NewAdapter(Config{
		BaseURL:            server.URL,
		StatsigMode:        "manual",
		StatsigManualValue: base64.RawStdEncoding.EncodeToString(make([]byte, 70)),
	}, manager, cipher, nil, nil)
	assetID, err := adapter.prepareVideoReferenceAudio(context.Background(), adapter.config(), lease, "test-sso", "Eve")
	if err != nil {
		t.Fatal(err)
	}
	if assetID != "4c671160-f2a1-45bc-bb26-15aa8eae2357" {
		t.Fatalf("asset ID = %q", assetID)
	}
	if uploads.Load() != 0 {
		t.Fatalf("voice_id triggered upload")
	}
	if imagines.Load() != 1 {
		t.Fatalf("imagine fetches = %d", imagines.Load())
	}
	again, err := adapter.prepareVideoReferenceAudio(context.Background(), adapter.config(), lease, "test-sso", "rex")
	if err != nil {
		t.Fatal(err)
	}
	if again != "1ec1c4ec-0c17-415e-b979-e4e4d483f41f" {
		t.Fatalf("rex asset ID = %q", again)
	}
	if imagines.Load() != 1 {
		t.Fatalf("cached imagine fetches = %d", imagines.Load())
	}
	direct, err := adapter.prepareVideoReferenceAudio(context.Background(), adapter.config(), lease, "test-sso", "c75a0ba4-0e2f-4a0b-b1f3-acee2d2a1887")
	if err != nil {
		t.Fatal(err)
	}
	if direct != "c75a0ba4-0e2f-4a0b-b1f3-acee2d2a1887" {
		t.Fatalf("uuid pass-through = %q", direct)
	}
	if _, err := adapter.prepareVideoReferenceAudio(context.Background(), adapter.config(), lease, "test-sso", "not-a-voice"); err == nil || !strings.Contains(err.Error(), "未知的 voice_id") {
		t.Fatalf("unknown voice error = %v", err)
	}
}

func mustParseCuratedHTML(t *testing.T) mediadomain.CuratedVoiceCatalog {
	t.Helper()
	raw, err := extractExperimentationScript([]byte(curatedVoiceHTML))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCuratedVoiceCatalogJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
