package media

import (
	"fmt"
	"math"
	"strings"
)

type VideoKeyframe struct {
	ImageURL   string  `json:"image_url"`
	TimestampS float64 `json:"timestamp_s"`
}

// ValidateVideoKeyframes 按官方时间网格校验内部锚点，不静默丢帧或改变时间。
func ValidateVideoKeyframes(frames []VideoKeyframe, duration int) error {
	if len(frames) > 4 {
		return fmt.Errorf("keyframes 最多 4 张")
	}
	slots := make(map[int]bool, len(frames))
	for i, frame := range frames {
		if strings.TrimSpace(frame.ImageURL) == "" || math.IsNaN(frame.TimestampS) || math.IsInf(frame.TimestampS, 0) ||
			frame.TimestampS <= 0 || frame.TimestampS >= float64(duration) {
			return fmt.Errorf("keyframes[%d] 必须提供图片，且 0 < timestamp_s < duration", i)
		}
		slot := int(math.Round(frame.TimestampS * 3))
		if slot <= 0 || slot >= duration*3 || slots[slot] {
			return fmt.Errorf("keyframes[%d] 时间取整后与端点或其他关键帧重叠，请至少间隔 1/3 秒", i)
		}
		slots[slot] = true
	}
	return nil
}
