package web

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// HEX 公式来自 grok 懒加载模块 1645e3（cdn.grok.com/_next/static/chunks/38asg_axwuaew.js，
// 文件名每次发版会变）。进程不会拉这个模块，也不会解混淆。生产签名使用冻住的
// (seed, HEX) 对，只刷新时间戳；不要用页面 curves 现算覆盖。
//
// 定位 1645e3：
//  1. 首页 JS 里搜 1645e3 或 x-statsig-id，调用链是
//     3mvtz9g_*.js 的 lk() → e.A(4629918) → 38asg_*.js。
//  2. 浏览器打开 grok.com/imagine，钩 crypto.subtle.digest，明文里
//     obfiowerehiring 后面就是 HEX；meta grok-site-verification 是 seed。
//  3. 钩 Element.animate / Animation.currentTime 和 getComputedStyle，
//     命中 duration=4096 的空 div：color+transform 编成 HEX。
//  4. 源码里 X=W=>{ let[f,d]=[W[12]%16, W[8]%16*W[20]%16*W[29]%16] }
//     然后 N(el, segments[f], d)，currentTime=round(d/10)*10。
//
// 下次 code 7 且 curves 已刷新仍失败时，用仓库技能 statsig-hex-repair
// （.grok/skills/statsig-hex-repair/）抓同一页对照并改下标。
// 旧 aurora 用 seed[5]%16 和 seed[22/23/24]，不要回退。

// 当前构建下的 4 条 Statsig SVG 路径。启动后若从 grok.com 页面 curves 抓到新路径会覆盖。
var (
	statsigSVGMu    sync.RWMutex
	statsigSVGPaths = [4]string{
		"M 10,30 C 202,167 243,238 50,9 h 101 s 53,236 92,62 C 183,211 231,32 79,32 h 212 s 177,182 47,35 C 239,79 13,166 84,159 h 3 s 52,122 82,64 C 135,46 167,243 207,93 h 185 s 53,51 174,249 C 167,105 13,127 93,97 h 1 s 82,247 113,216 C 141,225 31,57 85,81 h 224 s 89,74 87,116 C 72,183 62,34 48,21 h 55 s 0,5 124,62 C 6,158 39,101 63,253 h 45 s 152,200 201,164 C 53,182 133,87 119,220 h 255 s 138,213 214,18 C 62,247 43,239 182,13 h 107 s 238,188 198,254 C 169,156 237,209 230,249 h 73 s 22,110 87,116 C 231,172 154,252 178,106 h 94 s 13,30 102,215 C 206,110 66,71 157,77 h 126 s 94,77 102,79 C 123,221 171,198 227,123 h 94 s 49,65 222,147 C 58,201 175,209 43,247 h 95 s 26,25 43,80 C 180,184 254,148 197,87 h 123 s 227,38 117,121",
		"M 10,30 C 92,107 91,107 142,29 h 68 s 233,240 13,201 C 19,199 166,96 63,57 h 116 s 91,60 199,167 C 230,86 249,188 55,149 h 118 s 143,140 162,123 C 66,147 47,218 150,219 h 11 s 145,98 109,188 C 85,21 94,98 30,50 h 108 s 236,209 212,112 C 13,159 86,94 144,108 h 158 s 72,153 197,58 C 183,106 50,213 101,55 h 55 s 226,12 55,210 C 51,20 118,72 246,96 h 202 s 101,226 25,12 C 67,72 185,208 125,5 h 126 s 232,180 168,186 C 130,183 245,29 129,147 h 78 s 170,177 94,171 C 221,218 78,109 249,20 h 112 s 67,193 57,67 C 39,10 185,85 67,185 h 48 s 41,212 26,130 C 230,197 75,102 224,72 h 253 s 198,95 26,233 C 212,229 210,89 221,10 h 106 s 179,235 187,171 C 27,188 63,70 111,192 h 129 s 255,219 70,128 C 253,76 97,104 163,163 h 148 s 100,85 83,62",
		"M 10,30 C 3,142 215,98 78,231 h 145 s 226,98 100,95 C 176,15 12,17 28,42 h 115 s 94,179 227,198 C 138,151 125,127 137,1 h 182 s 139,246 224,5 C 100,182 243,133 120,6 h 152 s 240,164 96,85 C 78,216 22,78 188,239 h 19 s 44,188 41,17 C 102,116 224,115 28,219 h 237 s 123,38 184,218 C 70,113 93,123 243,8 h 110 s 44,219 143,252 C 193,139 11,47 183,27 h 162 s 191,97 238,138 C 203,96 186,119 113,6 h 241 s 62,45 35,239 C 189,194 24,103 180,203 h 156 s 229,76 226,172 C 232,84 123,215 86,104 h 109 s 177,207 71,244 C 215,49 76,4 159,174 h 169 s 64,10 128,177 C 22,51 158,116 100,105 h 1 s 83,84 53,217 C 30,61 199,197 127,151 h 76 s 90,130 88,80 C 132,156 76,146 33,243 h 7 s 8,169 171,76 C 92,39 69,45 49,88 h 85 s 47,126 99,148",
		"M 10,30 C 178,193 89,90 151,230 h 210 s 244,77 131,241 C 102,209 131,165 195,30 h 25 s 85,9 63,36 C 238,9 143,122 31,41 h 2 s 186,229 51,90 C 18,55 158,218 95,251 h 248 s 123,109 230,184 C 122,131 1,68 238,208 h 71 s 14,163 83,225 C 253,129 180,244 38,128 h 59 s 180,236 186,196 C 97,224 77,112 185,101 h 65 s 166,74 122,75 C 154,48 234,123 189,73 h 22 s 73,182 240,221 C 182,117 85,49 70,210 h 224 s 48,77 129,228 C 95,211 107,7 38,16 h 121 s 197,246 38,251 C 59,122 179,174 253,240 h 8 s 105,118 112,109 C 176,43 53,77 35,212 h 206 s 234,125 154,48 C 142,249 25,47 131,193 h 0 s 250,142 226,5 C 232,212 169,164 59,165 h 180 s 65,4 169,37 C 72,23 178,141 222,243 h 91 s 98,9 24,246 C 141,146 48,50 204,11 h 232 s 80,11 207,95",
	}
)

const statsigAnimationDuration = 4096.0

func replaceStatsigSVGPaths(paths []string) int {
	var next [4]string
	copied := 0
	for i := 0; i < 4 && i < len(paths); i++ {
		if strings.HasPrefix(strings.TrimSpace(paths[i]), "M 10,30 C") {
			next[i] = paths[i]
			copied++
		}
	}
	if copied < 4 {
		return 0
	}
	statsigSVGMu.Lock()
	statsigSVGPaths = next
	statsigSVGMu.Unlock()
	return copied
}

func computeStatsigHEXForSeed(seed []byte) (string, error) {
	statsigSVGMu.RLock()
	paths := statsigSVGPaths
	statsigSVGMu.RUnlock()
	return computeStatsigHEXForSeedWithPaths(seed, paths)
}

func computeStatsigHEXForSeedWithPaths(seed []byte, paths [4]string) (string, error) {
	if len(seed) < 30 {
		return "", fmt.Errorf("Statsig seed 过短")
	}
	path := paths[int(seed[5])%len(paths)]
	if path == "" {
		return "", fmt.Errorf("Statsig SVG 路径缺失")
	}
	segments := statsigPathNumberSegments(path)
	if len(segments) == 0 {
		return "", fmt.Errorf("Statsig SVG 路径无效")
	}
	// 1645e3：路径 seed[5]%4，段 seed[12]%16，seek (seed[8]%16)*(seed[20]%16)*(seed[29]%16) 再对齐到 10。
	segIdx := int(seed[12]) % 16
	if segIdx >= len(segments) {
		return "", fmt.Errorf("Statsig SVG 段越界")
	}
	seg := segments[segIdx]
	if len(seg) < 11 {
		return "", fmt.Errorf("Statsig SVG 段过短")
	}
	startColor := [3]float64{seg[0], seg[1], seg[2]}
	endColor := [3]float64{seg[3], seg[4], seg[5]}
	endAngle := statsigScaleValue(seg[6], 60, 360, true)
	x1 := statsigScaleValue(seg[7], 0, 1, false)
	y1 := statsigScaleValue(seg[8], -1, 1, false)
	x2 := statsigScaleValue(seg[9], 0, 1, false)
	y2 := statsigScaleValue(seg[10], -1, 1, false)
	seek := math.Round(float64((int(seed[8])%16)*(int(seed[20])%16)*(int(seed[29])%16))/10) * 10
	progress := statsigCubicBezierY(x1, y1, x2, y2, seek/statsigAnimationDuration)
	values := []float64{
		float64(statsigColorChannel(startColor[0], endColor[0], progress)),
		float64(statsigColorChannel(startColor[1], endColor[1], progress)),
		float64(statsigColorChannel(startColor[2], endColor[2], progress)),
		math.Cos(endAngle * progress * math.Pi / 180),
		math.Sin(endAngle * progress * math.Pi / 180),
	}
	values = append(values, -values[4], values[3], 0, 0)
	var buf strings.Builder
	for _, value := range values {
		buf.WriteString(statsigNumberToHex(statsigToFixed(value, 2)))
	}
	return strings.NewReplacer(".", "", "-", "").Replace(buf.String()), nil
}

func statsigPathNumberSegments(path string) [][]float64 {
	if len(path) <= 9 {
		return nil
	}
	parts := strings.Split(path[9:], "C")
	segments := make([][]float64, 0, len(parts))
	for _, part := range parts {
		nums := statsigExtractNumbers(part)
		if len(nums) > 0 {
			segments = append(segments, nums)
		}
	}
	return segments
}

func statsigExtractNumbers(seg string) []float64 {
	// 对齐 grok / aurora：seg.replace(/[^\d]+/g, " ")，点号和负号都是分隔符。
	var b strings.Builder
	inSpace := true
	for _, r := range seg {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			inSpace = false
			continue
		}
		if !inSpace {
			b.WriteByte(' ')
			inSpace = true
		}
	}
	parts := strings.Fields(b.String())
	nums := make([]float64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil {
			continue
		}
		nums = append(nums, value)
	}
	return nums
}

func statsigNumberToHex(n float64) string {
	if n == 0 {
		return "0"
	}
	if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
		return strconv.FormatInt(int64(n), 16)
	}
	return statsigFloatToHexJS(n)
}

// statsigFloatToHexJS 对齐 JS Number.prototype.toString(16)。
func statsigFloatToHexJS(n float64) string {
	if n < 0 {
		return "-" + statsigFloatToHexJS(-n)
	}
	if n == 0 {
		return "0"
	}
	intPart := uint64(n)
	var buf strings.Builder
	buf.WriteString(strconv.FormatUint(intPart, 16))
	frac := n - float64(intPart)
	if frac > 0 {
		digits := make([]byte, 0, 16)
		for i := 0; i < 16 && frac > 1e-18; i++ {
			frac *= 16
			digit := uint64(frac + 1e-12)
			if digit > 15 {
				digit = 15
			}
			digits = append(digits, "0123456789abcdef"[digit])
			frac -= float64(digit)
			if frac < 0 {
				frac = 0
			}
		}
		for len(digits) > 0 && digits[len(digits)-1] == '0' {
			digits = digits[:len(digits)-1]
		}
		if len(digits) > 0 {
			buf.WriteByte('.')
			buf.Write(digits)
		}
	}
	return buf.String()
}

func statsigScaleValue(n, min, max float64, floor bool) float64 {
	v := n*((max-min)/255) + min
	if floor {
		return math.Floor(v)
	}
	return statsigToFixed(v, 2)
}

func statsigColorChannel(start, end, progress float64) int {
	v := math.Round(start + (end-start)*progress)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return int(v)
}

func statsigToFixed(v float64, precision int) float64 {
	pow := math.Pow10(precision)
	return math.Round(v*pow) / pow
}

func statsigCubicBezierY(x1, y1, x2, y2, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	t := x
	for i := 0; i < 8; i++ {
		xAtT := statsigSampleCubic(t, x1, x2) - x
		if math.Abs(xAtT) < 1e-7 {
			return statsigSampleCubic(t, y1, y2)
		}
		d := statsigSampleCubicDerivative(t, x1, x2)
		if math.Abs(d) < 1e-7 {
			break
		}
		t -= xAtT / d
	}
	lo, hi := 0.0, 1.0
	t = x
	for lo < hi {
		xAtT := statsigSampleCubic(t, x1, x2)
		if math.Abs(xAtT-x) < 1e-7 {
			return statsigSampleCubic(t, y1, y2)
		}
		if x > xAtT {
			lo = t
		} else {
			hi = t
		}
		t = (hi + lo) / 2
	}
	return statsigSampleCubic(t, y1, y2)
}

func statsigSampleCubic(t, a1, a2 float64) float64 {
	return ((1-3*a2+3*a1)*t+(3*a2-6*a1))*t*t + 3*a1*t
}

func statsigSampleCubicDerivative(t, a1, a2 float64) float64 {
	return (3*(1-3*a2+3*a1)*t+2*(3*a2-6*a1))*t + 3*a1
}
