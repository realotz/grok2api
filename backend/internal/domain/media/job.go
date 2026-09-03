package media

import (
	"strings"
	"time"
)

type Status string

const (
	StatusQueued     Status = "queued"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// MaxInputJSONBytes is the persisted media_jobs.input_json ceiling (32 MiB).
// Keep the relational CHECK constraint and gateway encode guard aligned with this value.
const MaxInputJSONBytes = 32 << 20

// MaxInputImages 是旧图片和视频模型保留的参考图上限。
const MaxInputImages = 8

// MaxModelReferenceImages 是 1.5 视频和 2.0 图片模型的参考图上限。
const MaxModelReferenceImages = 14

// MaxPersistedInputImages 是视频任务的绝对存储上限，模型限制在入库前单独校验。
const MaxPersistedInputImages = MaxModelReferenceImages

// ReferenceImageLimit 根据公开或上游模型名返回参考图上限，其他模型保持旧限制。
func ReferenceImageLimit(model string) int {
	value := strings.ToLower(strings.TrimSpace(model))
	if index := strings.LastIndex(value, "/"); index >= 0 {
		value = strings.TrimSpace(value[index+1:])
	}
	switch value {
	case "grok-imagine-video-1.5", "grok-imagine-image-2.0", "grok-imagine-image-2.0-web":
		return MaxModelReferenceImages
	default:
		return MaxInputImages
	}
}

// MaxInputAssetBytes limits each temporary image or video input to 20 MiB.
const MaxInputAssetBytes = 20 << 20

type VideoOperation string

const (
	VideoOperationGenerate VideoOperation = "generate"
	VideoOperationEdit     VideoOperation = "edit"
	VideoOperationExtend   VideoOperation = "extend"
)

// Job 表示可跨进程重启恢复的异步视频任务。
type Job struct {
	ID              string
	RequestID       string
	ClientKeyID     uint64
	ClientKeyName   string
	ClientIP        string
	AccountID       uint64
	AccountName     string
	EgressNodeID    *uint64
	EgressNodeName  string
	EgressScope     string
	EgressMode      string
	Provider        string
	Model           string
	ModelRouteID    uint64
	UpstreamModel   string
	Operation       VideoOperation
	Prompt          string
	Seconds         int
	Size            string
	Quality         string
	Status          Status
	Progress        int
	InputJSON       string
	InputImageCount int
	UpstreamURL     string
	// ResultAssetID 指向本地媒体资产；XAI ZDR 上传完成后优先从此读取。
	ResultAssetID   string
	ContentType     string
	ErrorCode       string
	ErrorMessage    string
	LeaseUntil      *time.Time
	ClaimToken      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
	UsageRecordedAt *time.Time
}
