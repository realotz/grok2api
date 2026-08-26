package ssorisk

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

func firstGRPCWebMessage(body []byte) ([]byte, string, error) {
	message, grpcStatus, err := parseGRPCWebFrames(body)
	if err != nil {
		return nil, "", err
	}
	return message, grpcStatus, nil
}

func parseGRPCWebFrames(body []byte) ([]byte, string, error) {
	var message []byte
	grpcStatus := ""
	for len(body) > 0 {
		if len(body) < 5 {
			return nil, "", fmt.Errorf("gRPC-Web 响应包含不完整帧头")
		}
		flag := body[0]
		length := int(binary.BigEndian.Uint32(body[1:5]))
		body = body[5:]
		if length < 0 || length > len(body) {
			return nil, "", fmt.Errorf("gRPC-Web 帧长度无效")
		}
		payload := body[:length]
		body = body[length:]
		if flag&0x80 == 0 {
			if flag != 0 {
				return nil, "", fmt.Errorf("不支持压缩的 gRPC-Web 响应")
			}
			if message == nil {
				message = append([]byte(nil), payload...)
			}
			continue
		}
		for _, line := range bytes.Split(payload, []byte{'\n'}) {
			name, value, ok := bytes.Cut(bytes.TrimSpace(line), []byte{':'})
			if ok && string(bytes.ToLower(bytes.TrimSpace(name))) == "grpc-status" {
				grpcStatus = string(bytes.TrimSpace(value))
			}
		}
	}
	return message, grpcStatus, nil
}
