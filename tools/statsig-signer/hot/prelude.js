// grok.com x-statsig HEX. 签名器用 Node vm eval 这段脚本。
// 必须提供 computeHex(seedBuffer, paths) -> string。
// 当前对照 1645e3：path seed[5]%4，seg seed[39]%16，
// seek (seed[3]%16)*(seed[31]%16)*(seed[36]%16) 再对齐到 10。

function extractNumbers(seg) {
  return String(seg)
    .replace(/[^\d]+/g, " ")
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .map(Number);
}

function pathSegments(path) {
  if (path.length <= 9) return [];
  return path
    .slice(9)
    .split("C")
    .map(extractNumbers)
    .filter((nums) => nums.length);
}

function toFixed2(v) {
  return Math.round(v * 100) / 100;
}

function scaleValue(n, min, max, floor) {
  const value = n * ((max - min) / 255) + min;
  return floor ? Math.floor(value) : toFixed2(value);
}

function colorChannel(start, end, progress) {
  const v = Math.round(start + (end - start) * progress);
  return Math.max(0, Math.min(255, v));
}

function sampleCubic(t, a1, a2) {
  return ((1 - 3 * a2 + 3 * a1) * t + (3 * a2 - 6 * a1)) * t * t + 3 * a1 * t;
}

function sampleCubicDerivative(t, a1, a2) {
  return (3 * (1 - 3 * a2 + 3 * a1) * t + 2 * (3 * a2 - 6 * a1)) * t + 3 * a1;
}

function cubicBezierY(x1, y1, x2, y2, x) {
  if (x <= 0) return 0;
  if (x >= 1) return 1;
  let t = x;
  for (let i = 0; i < 8; i++) {
    const xAtT = sampleCubic(t, x1, x2) - x;
    if (Math.abs(xAtT) < 1e-7) return sampleCubic(t, y1, y2);
    const d = sampleCubicDerivative(t, x1, x2);
    if (Math.abs(d) < 1e-7) break;
    t -= xAtT / d;
  }
  let lo = 0;
  let hi = 1;
  t = x;
  for (let i = 0; i < 32; i++) {
    const xAtT = sampleCubic(t, x1, x2);
    if (Math.abs(xAtT - x) < 1e-7) return sampleCubic(t, y1, y2);
    if (x > xAtT) lo = t;
    else hi = t;
    const next = (hi + lo) / 2;
    if (next === t) break;
    t = next;
  }
  return sampleCubic(t, y1, y2);
}

function hexFromSegment(seg, seek, duration) {
  if (seg.length < 11) throw new Error("Statsig SVG 段过短");
  const endAngle = scaleValue(seg[6], 60, 360, true);
  const x1 = scaleValue(seg[7], 0, 1, false);
  const y1 = scaleValue(seg[8], -1, 1, false);
  const x2 = scaleValue(seg[9], 0, 1, false);
  const y2 = scaleValue(seg[10], -1, 1, false);
  const progress = cubicBezierY(x1, y1, x2, y2, seek / duration);
  const cosV = Math.cos((endAngle * progress * Math.PI) / 180);
  const sinV = Math.sin((endAngle * progress * Math.PI) / 180);
  const values = [
    colorChannel(seg[0], seg[3], progress),
    colorChannel(seg[1], seg[4], progress),
    colorChannel(seg[2], seg[5], progress),
    cosV,
    sinV,
    -sinV,
    cosV,
    0,
    0,
  ];
  return values
    .map((value) => Number(toFixed2(value)).toString(16))
    .join("")
    .replace(/[.-]/g, "");
}
