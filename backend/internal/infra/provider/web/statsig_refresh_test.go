package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestExtractStatsigScriptURLs(t *testing.T) {
	html := `<script src="https://cdn.grok.com/_next/static/chunks/abc.js"></script><script src="https://example.com/skip.js"></script>`
	urls := extractStatsigScriptURLs(html)
	if len(urls) != 1 || !strings.HasSuffix(urls[0], "/abc.js") {
		t.Fatalf("urls = %#v", urls)
	}
}

func TestCollectStatsigSVGPathsFindsPathsPastFirstEightScripts(t *testing.T) {
	var html strings.Builder
	bodies := map[string]string{}
	for i := 0; i < 10; i++ {
		url := "https://cdn.grok.com/_next/static/chunks/" + string(rune('a'+i)) + ".js"
		html.WriteString(`<script src="` + url + `"></script>`)
		if i < 8 {
			bodies[url] = `console.log("no path here")`
			continue
		}
	}
	bodies["https://cdn.grok.com/_next/static/chunks/i.js"] = `d:"` + statsigSVGPaths[0] + `",d:"` + statsigSVGPaths[1] + `"`
	bodies["https://cdn.grok.com/_next/static/chunks/j.js"] = `d:"` + statsigSVGPaths[2] + `",d:"` + statsigSVGPaths[3] + `"`
	paths := collectStatsigSVGPaths(html.String(), func(url string) (string, bool) {
		body, ok := bodies[url]
		return body, ok
	})
	if len(paths) != 4 {
		t.Fatalf("paths = %d %#v", len(paths), paths)
	}
	for i, path := range paths {
		if !strings.HasPrefix(path, "M 10,30 C") || path != statsigSVGPaths[i] {
			t.Fatalf("path[%d] = %q", i, path)
		}
	}
}

func TestExtractStatsigCurvePathsFromRSC(t *testing.T) {
	html := `self.__next_f.push([1,"72:[\"$\",\"$L92\",null,{\"curves\":[[{\"color\":[184,215,9,103,127,98],\"deg\":109,\"bezier\":[207,104,82,218]},{\"color\":[28,107,43,182,205,177],\"deg\":154,\"bezier\":[41,84,209,173]},{\"color\":[255,13,110,30,60,43],\"deg\":101,\"bezier\":[103,114,63,146]},{\"color\":[253,35,14,44,70,183],\"deg\":216,\"bezier\":[23,211,99,90]}],[{\"color\":[135,39,118,57,222,236],\"deg\":253,\"bezier\":[220,83,85,23]},{\"color\":[7,250,25,81,25,91],\"deg\":10,\"bezier\":[1,2,3,4]},{\"color\":[1,2,3,4,5,6],\"deg\":7,\"bezier\":[8,9,10,11]},{\"color\":[12,13,14,15,16,17],\"deg\":18,\"bezier\":[19,20,21,22]}],[{\"color\":[191,237,141,129,193,104],\"deg\":252,\"bezier\":[151,38,4,210]},{\"color\":[242,102,234,138,48,243],\"deg\":1,\"bezier\":[2,3,4,5]},{\"color\":[6,7,8,9,10,11],\"deg\":12,\"bezier\":[13,14,15,16]},{\"color\":[17,18,19,20,21,22],\"deg\":23,\"bezier\":[24,25,26,27]}],[{\"color\":[62,98,216,245,145,241],\"deg\":174,\"bezier\":[71,47,86,242]},{\"color\":[59,49,28,27,118,61],\"deg\":176,\"bezier\":[1,2,3,4]},{\"color\":[5,6,7,8,9,10],\"deg\":11,\"bezier\":[12,13,14,15]},{\"color\":[16,17,18,19,20,21],\"deg\":22,\"bezier\":[23,24,25,26]}]]}\n"])`
	paths := extractStatsigCurvePaths(html)
	if len(paths) != 4 {
		t.Fatalf("paths = %d %#v", len(paths), paths)
	}
	if !strings.HasPrefix(paths[0], "M 10,30 C 184,215 9,103 127,98 h 109 s 207,104 82,218") {
		t.Fatalf("path0 = %q", paths[0])
	}
	if strings.Count(paths[3], "C") < 4 {
		t.Fatalf("path3 C-count = %d", strings.Count(paths[3], "C"))
	}
}

func TestStatsigExtractNumbersSplitsNonDigits(t *testing.T) {
	got := statsigExtractNumbers(" 6.48 -12.222 5 ")
	want := []float64{6, 48, 12, 222, 5}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}

// 同一页抓到的 seed+curves+官方 HEX。1645e3 下标改了就换这份 fixture 并改 statsig_hex.go。
func TestComputeStatsigHEXMatchesLiveSamePageCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/statsig_live_pair.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Seed  string   `json:"seed"`
		HEX   string   `json:"hex"`
		Paths []string `json:"paths"`
	}
	if json.Unmarshal(raw, &fixture) != nil || len(fixture.Paths) < 4 {
		t.Fatalf("fixture invalid")
	}
	seed, err := decodeStatsigSeed(fixture.Seed)
	if err != nil {
		t.Fatal(err)
	}
	var paths [4]string
	copy(paths[:], fixture.Paths[:4])
	hex, err := computeStatsigHEXForSeedWithPaths(seed, paths)
	if err != nil {
		t.Fatal(err)
	}
	if hex != fixture.HEX {
		t.Fatalf("hex=%q want=%q", hex, fixture.HEX)
	}
}

func TestExtractStatsigSVGPaths(t *testing.T) {
	source := `d:"` + statsigSVGPaths[0] + `"`
	paths := extractStatsigSVGPaths(source)
	if len(paths) != 1 || !strings.HasPrefix(paths[0], "M 10,30 C") {
		t.Fatalf("paths = %#v", paths)
	}
	if short := extractStatsigSVGPaths(`M 10,30 C 1,2`); len(short) != 0 {
		t.Fatalf("short path should be ignored: %#v", short)
	}
}

func TestIsStatsigAntiBotJSON(t *testing.T) {
	forbidden := &webMediaUpstreamError{status: http.StatusForbidden, bodyKind: "json"}
	if !isStatsigRefreshableMediaError(forbidden, []byte(`{"error":{"message":"Request rejected by anti-bot rules.","code":7}}`)) {
		t.Fatal("expected anti-bot JSON")
	}
	if !isStatsigRefreshableMediaError(forbidden, []byte(`{"error":{"code":7,"message":"This page is out of date. Reload to continue."}}`)) {
		t.Fatal("expected nested out-of-date JSON")
	}
	if !isStatsigRefreshableMediaError(forbidden, []byte(`{"code":7,"message":"This page is out of date. Reload to continue.","details":[]}`)) {
		t.Fatal("expected root out-of-date JSON")
	}
	if isStatsigRefreshableMediaError(forbidden, []byte(`{"error":{"message":"User is blocked [WKE=unauthorized:blocked-user]","code":7}}`)) {
		t.Fatal("account-block JSON must not be treated as Statsig refresh")
	}
	if isStatsigAntiBotJSON([]byte(`{"error":{"message":"User is blocked [WKE=unauthorized:blocked-user]","code":7}}`)) {
		t.Fatal("account-block JSON must not be treated as Statsig refresh")
	}
}

func TestComputeStatsigHEXForKnownSeedIsSelfConsistent(t *testing.T) {
	seed, err := decodeStatsigSeed("YGVPoGJ3OkuqXVlKrsPF/2PeV4XTAdWFB6r4pSiisYmG5JdDL56wT3Qvh8nzt/WF")
	if err != nil {
		t.Fatal(err)
	}
	hex, err := computeStatsigHEXForSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(hex) < 32 {
		t.Fatalf("hex too short: %q", hex)
	}
	again, err := computeStatsigHEXForSeed(seed)
	if err != nil || again != hex {
		t.Fatalf("unstable hex %q vs %q err=%v", hex, again, err)
	}
}

func TestStatsigNumberToHexMatchesJS(t *testing.T) {
	if got := statsigNumberToHex(185); got != "b9" {
		t.Fatalf("185 = %q", got)
	}
	if got := statsigNumberToHex(0.24); got != "0.3d70a3d70a3d7" {
		t.Fatalf("0.24 = %q", got)
	}
	if got := statsigNumberToHex(0.97); got != "0.f851eb851eb85" {
		t.Fatalf("0.97 = %q", got)
	}
	if got := statsigNumberToHex(0.01); got != "0.028f5c28f5c28f6" {
		t.Fatalf("0.01 = %q", got)
	}
	if got := statsigNumberToHex(0); got != "0" {
		t.Fatalf("0 = %q", got)
	}
}

func TestComputeStatsigHEXForSeedStable(t *testing.T) {
	seed := make([]byte, 48)
	seed[5] = 1
	seed[22] = 2
	seed[23] = 3
	seed[24] = 4
	first, err := computeStatsigHEXForSeed(seed)
	if err != nil || first == "" {
		t.Fatalf("hex=%q err=%v", first, err)
	}
	second, err := computeStatsigHEXForSeed(seed)
	if err != nil || second != first {
		t.Fatalf("unstable hex %q vs %q err=%v", first, second, err)
	}
}

func TestRefreshStatsigPairDoesNotOverwriteFrozenPair(t *testing.T) {
	t.Cleanup(resetStatsigRefreshState)
	seed, hex, err := currentStatsigPair()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{cfg: Config{BaseURL: "https://grok.com"}}
	if err := adapter.refreshStatsigPair(context.Background(), "token", nil); err != nil {
		t.Fatal(err)
	}
	againSeed, againHEX, err := currentStatsigPair()
	if err != nil {
		t.Fatal(err)
	}
	if string(againSeed) != string(seed) || againHEX != hex {
		t.Fatalf("frozen pair was overwritten")
	}
	if statsigPairNeedsRefresh() {
		t.Fatal("refresh should clear the stale flag without replacing the pair")
	}
}

func TestRefreshStatsigPairFromHTML(t *testing.T) {
	seed := make([]byte, 48)
	seed[5] = 2
	encoded := base64.StdEncoding.EncodeToString(seed)
	html := []byte(`<html><head><meta name="grok-site-verification" content="` + encoded + `"/></head></html>`)
	content, err := extractStatsigMetaContent(html)
	if err != nil || content != encoded {
		t.Fatalf("meta=%q err=%v", content, err)
	}
	decoded, err := decodeStatsigSeed(content)
	if err != nil {
		t.Fatal(err)
	}
	hex, err := computeStatsigHEXForSeed(decoded)
	if err != nil || hex == "" {
		t.Fatalf("hex=%q err=%v", hex, err)
	}
	if err := setStatsigPair(decoded, hex); err != nil {
		t.Fatal(err)
	}
	id, err := generateLocalStatsig("POST", "/rest/app-chat/conversations/new", 1_800_000_000)
	if err != nil || !validStatsigID(id) {
		t.Fatalf("id=%q err=%v", id, err)
	}
}
