package settings

import "testing"

func TestNormalizeSSOVideoRiskEver(t *testing.T) {
	if got := NormalizeSSOVideoRiskEver(""); got != SSOVideoRiskEverAuto {
		t.Fatalf("empty = %q", got)
	}
	if got := NormalizeSSOVideoRiskEver(" AUTO "); got != SSOVideoRiskEverAuto {
		t.Fatalf("auto = %q", got)
	}
	if got := NormalizeSSOVideoRiskEver("on"); got != SSOVideoRiskEverOn {
		t.Fatalf("on = %q", got)
	}
	if got := NormalizeSSOVideoRiskEver("off"); got != SSOVideoRiskEverOff {
		t.Fatalf("off = %q", got)
	}
	if !ExcludeSSOVideoRiskEver("") || !ExcludeSSOVideoRiskEver("auto") || !ExcludeSSOVideoRiskEver("on") {
		t.Fatal("auto/on must exclude historical risk from video")
	}
	if ExcludeSSOVideoRiskEver("off") {
		t.Fatal("off must allow historical-risk accounts")
	}
	if !ValidSSOVideoRiskEver("") || !ValidSSOVideoRiskEver("auto") || ValidSSOVideoRiskEver("maybe") {
		t.Fatal("validity mismatch")
	}
}
