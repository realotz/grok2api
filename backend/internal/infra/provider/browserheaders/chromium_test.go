package browserheaders

import (
	"net/http"
	"strings"
	"testing"
)

func TestApplyChromiumLowEntropyHintsMatchesChromeUA(t *testing.T) {
	header := make(http.Header)
	ApplyChromiumLowEntropyHints(header, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	if header.Get("Sec-Ch-Ua-Platform") != `"macOS"` || header.Get("Sec-Ch-Ua-Mobile") != "?0" {
		t.Fatalf("hints = %#v", header)
	}
	if !strings.Contains(header.Get("Sec-Ch-Ua"), "Google Chrome") || !strings.Contains(header.Get("Sec-Ch-Ua"), `v="146"`) {
		t.Fatalf("sec-ch-ua = %q", header.Get("Sec-Ch-Ua"))
	}
	if header.Get("Sec-Ch-Ua-Arch") != "" {
		t.Fatal("low-entropy helper must not set arch")
	}
}

func TestApplyChromiumLowEntropyHintsSkipsSafari(t *testing.T) {
	header := make(http.Header)
	ApplyChromiumLowEntropyHints(header, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Version/18.0 Safari/605.1.15")
	if len(header) != 0 {
		t.Fatalf("unexpected hints %#v", header)
	}
}
