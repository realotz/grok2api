package gateway

import (
	"encoding/json"
	"github.com/chenyme/grok2api/backend/internal/domain/media"
)

// decodeVideoKeyframes 从持久任务恢复独立的时间锚点，不混入风格参考图。
func decodeVideoKeyframes(raw string) []media.VideoKeyframe {
	var input struct {
		Keyframes []media.VideoKeyframe `json:"keyframes"`
	}
	_ = json.Unmarshal([]byte(raw), &input)
	return input.Keyframes
}
