package provider

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type videoStageStatusError struct{ status int }

func (e videoStageStatusError) Error() string       { return http.StatusText(e.status) }
func (e videoStageStatusError) HTTPStatusCode() int { return e.status }

func TestVideoCreateFailureStageIsFailClosed(t *testing.T) {
	if stage := VideoCreateFailureStage(errors.New("connection reset after write")); stage != VideoStageSubmitted {
		t.Fatalf("transport failure stage = %q", stage)
	}
	if stage := VideoCreateFailureStage(fmt.Errorf("wrapped: %w", videoStageStatusError{status: http.StatusTooManyRequests})); stage != VideoStageCreate {
		t.Fatalf("explicit 429 stage = %q", stage)
	}
	if stage := VideoCreateFailureStage(ErrUnauthorized); stage != VideoStageCreate {
		t.Fatalf("explicit unauthorized stage = %q", stage)
	}
	if stage := VideoCreateFailureStage(videoStageStatusError{status: http.StatusInternalServerError}); stage != VideoStageSubmitted {
		t.Fatalf("explicit 500 stage = %q", stage)
	}
}

func TestIsFastRemoteVideoRisk(t *testing.T) {
	t.Parallel()
	remote := VideoResult{URL: "https://cdn.example/scenery.mp4"}
	if !IsFastRemoteVideoRisk(time.Second, remote) {
		t.Fatal("1s remote video should be risk")
	}
	if IsFastRemoteVideoRisk(VideoRiskReadyWithin, remote) {
		t.Fatal("exactly 10s should not be risk")
	}
	if IsFastRemoteVideoRisk(11*time.Second, remote) {
		t.Fatal("slow remote video should not be risk")
	}
	if IsFastRemoteVideoRisk(time.Second, VideoResult{URL: "https://cdn.example/a.mp4", AssetID: "vid_local"}) {
		t.Fatal("local asset must not be treated as scenery")
	}
	if IsFastRemoteVideoRisk(time.Second, VideoResult{}) {
		t.Fatal("empty result should not be risk")
	}
}

func TestIsFastImageRisk(t *testing.T) {
	t.Parallel()
	if !IsFastImageRisk(time.Second) {
		t.Fatal("1s image should be risk")
	}
	if IsFastImageRisk(ImageRiskReadyWithin) {
		t.Fatal("exactly 10s should not be image risk")
	}
	if IsFastImageRisk(11 * time.Second) {
		t.Fatal("slow image should not be risk")
	}
}
