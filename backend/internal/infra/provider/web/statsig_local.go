package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
)

const (
	statsigEpoch = 1682924400
	statsigSalt  = "obfiowerehiring"
	statsigMark  = 0x03
)

// 内嵌一对已在真实 grok.com 会话里验证过的 (seed, HEX)。
// grok 只校验载荷自洽（HEX 与 seed 匹配、时间戳与 SHA 正确），不要求 seed 等于当前页面 meta。
var (
	statsigPairMu sync.RWMutex
	statsigSeed   []byte
	statsigHEX    string
)

func init() {
	seed, err := decodeStatsigSeed("YGVPoGJ3OkuqXVlKrsPF/2PeV4XTAdWFB6r4pSiisYmG5JdDL56wT3Qvh8nzt/WF")
	if err != nil || len(seed) != 48 {
		return
	}
	statsigSeed = seed
	statsigHEX = "b987850f851eb851eb8503d70a3d70a3d703d70a3d70a3d70f851eb851eb8500"
}

func decodeStatsigSeed(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func setStatsigPair(seed []byte, hex string) error {
	if len(seed) != 48 {
		return errors.New("Statsig seed 必须是 48 字节")
	}
	if strings.TrimSpace(hex) == "" {
		return errors.New("Statsig HEX 不能为空")
	}
	copied := append([]byte(nil), seed...)
	statsigPairMu.Lock()
	statsigSeed = copied
	statsigHEX = hex
	statsigPairMu.Unlock()
	return nil
}

func currentStatsigPair() ([]byte, string, error) {
	statsigPairMu.RLock()
	defer statsigPairMu.RUnlock()
	if len(statsigSeed) != 48 || statsigHEX == "" {
		return nil, "", errors.New("本地 Statsig 种子未初始化")
	}
	return append([]byte(nil), statsigSeed...), statsigHEX, nil
}

// generateLocalStatsig 按 grok.com 应用层算法现算 x-statsig-id。
// 载荷 70 字节：随机 key + XOR(seed) + XOR(uint32LE(now-epoch) + sha256[:16] + 0x03)。
func generateLocalStatsig(method, path string, nowUnix int64) (string, error) {
	seed, hex, err := currentStatsigPair()
	if err != nil {
		return "", err
	}
	return buildLocalStatsig(seed, hex, method, path, nowUnix)
}

func buildLocalStatsig(seed []byte, hex, method, path string, nowUnix int64) (string, error) {
	if len(seed) != 48 {
		return "", errors.New("Statsig seed 必须是 48 字节")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if method == "" || path == "" {
		return "", errors.New("Statsig 缺少 method 或 path")
	}
	number := uint32(nowUnix - statsigEpoch)
	var builder strings.Builder
	builder.Grow(len(method) + len(path) + len(hex) + 40)
	builder.WriteString(method)
	builder.WriteByte('!')
	builder.WriteString(path)
	builder.WriteByte('!')
	builder.WriteString(strconv.FormatUint(uint64(number), 10))
	builder.WriteString(statsigSalt)
	builder.WriteString(hex)
	sum := sha256.Sum256([]byte(builder.String()))

	var keyByte [1]byte
	if _, err := rand.Read(keyByte[:]); err != nil {
		return "", err
	}
	key := keyByte[0]
	out := make([]byte, 70)
	out[0] = key
	for i := 0; i < 48; i++ {
		out[1+i] = seed[i] ^ key
	}
	out[49] = byte(number) ^ key
	out[50] = byte(number>>8) ^ key
	out[51] = byte(number>>16) ^ key
	out[52] = byte(number>>24) ^ key
	for i := 0; i < 16; i++ {
		out[53+i] = sum[i] ^ key
	}
	out[69] = statsigMark ^ key
	return base64.RawStdEncoding.EncodeToString(out), nil
}
