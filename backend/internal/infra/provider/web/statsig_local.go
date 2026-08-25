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
// grok 校验 HEX 与当前前端 curves 对这个 seed 自洽，再加新鲜时间戳和 SHA；
// 不要求 seed 等于调用时的页面 meta。前端发版改了 curves 之后，旧 pair
// 会变成 403 code 7（This page is out of date），需要重新抓同一页的
// seed+官方 HEX 替换这里，而不是每次请求去拉首页。
//
// 2026-08-25 对照 grok-web@5971eb27；不要用 statsig_hex.go 现算覆盖。
var (
	statsigPairMu sync.RWMutex
	statsigSeed   []byte
	statsigHEX    string
)

func init() {
	seed, err := decodeStatsigSeed("o15XOuj/p4WHEnsnuxG+Z2qe9rMpftEEe8qeAyEfauBuejVfITaKQFXtwi641ycs")
	if err != nil || len(seed) != 48 {
		return
	}
	statsigSeed = seed
	statsigHEX = "8a50400f33333333333304ccccccccccccc04ccccccccccccc0f33333333333300"
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
