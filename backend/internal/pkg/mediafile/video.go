package mediafile

import (
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"strings"
)

// MP4DurationSeconds 只读取最终文件的 moov/mvhd 元数据，将成品总时长四舍五入为整数秒。
func MP4DurationSeconds(body io.ReaderAt, size int64) (int, error) {
	moov, moovSize, err := findMP4Box(body, 0, size, "moov")
	if err != nil {
		return 0, err
	}
	mvhd, mvhdSize, err := findMP4Box(body, moov, moov+moovSize, "mvhd")
	if err != nil {
		return 0, err
	}
	var header [32]byte
	if mvhdSize < 20 {
		return 0, fmt.Errorf("视频 movie header 不完整")
	}
	if _, err := body.ReadAt(header[:20], mvhd); err != nil {
		return 0, err
	}
	var scale, duration uint64
	switch header[0] {
	case 0:
		scale = uint64(binary.BigEndian.Uint32(header[12:16]))
		value := binary.BigEndian.Uint32(header[16:20])
		if value == ^uint32(0) {
			return 0, fmt.Errorf("视频时长未确定")
		}
		duration = uint64(value)
	case 1:
		if mvhdSize < int64(len(header)) {
			return 0, fmt.Errorf("视频 movie header 不完整")
		}
		if _, err := body.ReadAt(header[:], mvhd); err != nil {
			return 0, err
		}
		scale = uint64(binary.BigEndian.Uint32(header[20:24]))
		duration = binary.BigEndian.Uint64(header[24:32])
		if duration == ^uint64(0) {
			return 0, fmt.Errorf("视频时长未确定")
		}
	default:
		return 0, fmt.Errorf("不支持的视频 movie header 版本")
	}
	if scale == 0 || duration == 0 {
		return 0, fmt.Errorf("视频时长元数据无效")
	}
	seconds := duration / scale
	if duration%scale >= (scale+1)/2 {
		seconds++
	}
	if seconds == 0 || seconds > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("视频整数秒时长超出范围")
	}
	return int(seconds), nil
}

// findMP4Box 按父容器边界查找 box，跳过媒体正文并限制元数据扫描次数。
func findMP4Box(body io.ReaderAt, start, end int64, kind string) (int64, int64, error) {
	var header [16]byte
	for count := 0; start < end && count < 4096; count++ {
		if start < 0 || end-start < 8 {
			return 0, 0, fmt.Errorf("视频 box 边界无效")
		}
		if _, err := body.ReadAt(header[:8], start); err != nil {
			return 0, 0, err
		}
		size := uint64(binary.BigEndian.Uint32(header[:4]))
		headerSize := int64(8)
		switch size {
		case 0:
			size = uint64(end - start)
		case 1:
			if end-start < 16 {
				return 0, 0, fmt.Errorf("视频扩展 box header 不完整")
			}
			if _, err := body.ReadAt(header[8:], start+8); err != nil {
				return 0, 0, err
			}
			size = binary.BigEndian.Uint64(header[8:])
			headerSize = 16
		}
		if size < uint64(headerSize) || size > uint64(end-start) {
			return 0, 0, fmt.Errorf("视频 box 长度无效")
		}
		if string(header[4:8]) == kind {
			return start + headerSize, int64(size) - headerSize, nil
		}
		start += int64(size)
	}
	return 0, 0, fmt.Errorf("视频缺少 %s 元数据", kind)
}

// VideoExtension returns the canonical file extension for a supported video
// media type. Parameters such as codecs are ignored when selecting the suffix.
func VideoExtension(contentType string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(mediaType) {
	case "video/mp4":
		return ".mp4", true
	case "video/webm":
		return ".webm", true
	case "video/quicktime":
		return ".mov", true
	default:
		return "", false
	}
}

// VideoContentDisposition builds a safe inline filename for video responses.
// Public request and asset identifiers are restricted to a conservative ASCII
// subset before they are placed in an HTTP response header.
func VideoContentDisposition(name, contentType string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, strings.TrimSpace(name))
	if name == "" {
		name = "video"
	}
	if extension, ok := VideoExtension(contentType); ok {
		name += extension
	}
	return `inline; filename="` + name + `"`
}
