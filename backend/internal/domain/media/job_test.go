package media

import "testing"

func TestReferenceImageLimit(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{model: "grok-imagine-video-1.5", want: 14},
		{model: "Web/grok-imagine-video-1.5", want: 14},
		{model: "grok-imagine-image-2.0", want: 14},
		{model: "grok-imagine-image-2.0-web", want: 14},
		{model: "grok-imagine-video", want: 8},
		{model: "grok-imagine-image-edit", want: 8},
	}
	for _, test := range tests {
		if got := ReferenceImageLimit(test.model); got != test.want {
			t.Fatalf("ReferenceImageLimit(%q) = %d, want %d", test.model, got, test.want)
		}
	}
}
