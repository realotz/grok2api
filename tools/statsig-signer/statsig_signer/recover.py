from __future__ import annotations

from dataclasses import dataclass, field
from itertools import combinations_with_replacement, product
from typing import Any, Iterator, Sequence

from .algorithm import Formula, aligned_seek, compute_hex, go_round, hex_from_segment, path_segments

SEEK_MAX = 4090


@dataclass
class RecoverResult:
    status: str
    formula: Formula | None = None
    hex: str = ""
    reason: str = ""
    candidates: list[dict[str, Any]] = field(default_factory=list)
    candidate_count: int = 0


def recover_formula(
    seed: bytes,
    paths: Sequence[str],
    official_hex: str,
    current: Formula | None = None,
    seek_hint: float | None = None,
) -> RecoverResult:
    current = current or Formula()
    official = (official_hex or "").strip()
    if not official:
        return RecoverResult(status="failed", reason="缺少官方 HEX")
    try:
        got = compute_hex(seed, paths, current)
    except ValueError as exc:
        got = ""
        current_error = str(exc)
    else:
        current_error = ""
        if got == official:
            return RecoverResult(status="matched", formula=current, hex=got, reason="当前公式仍匹配官方 HEX")

    direct = _search_direct(seed, paths, official, seek_hint)
    if not direct:
        reason = "穷举 path/seg/seek 得不到官方 HEX"
        if current_error:
            reason += f"；当前公式: {current_error}"
        return RecoverResult(status="needs_agent", hex=official, reason=reason)

    inverted = _invert_indices(seed, current, direct)
    if inverted is None:
        return RecoverResult(
            status="needs_agent",
            hex=official,
            reason="找到了 path/seg/seek，但无法反解 seed 下标",
            candidates=[direct],
        )
    try:
        verified = compute_hex(seed, paths, inverted)
    except ValueError as exc:
        return RecoverResult(status="needs_agent", reason=f"反解公式无法复算: {exc}", candidates=[direct])
    if verified != official:
        return RecoverResult(status="needs_agent", reason="反解公式复算 HEX 不一致", candidates=[direct])
    return RecoverResult(status="recovered", formula=inverted, hex=verified, reason="穷举下标后与官方 HEX 一致")


def recover_formula_stable(
    samples: Sequence[dict[str, Any]],
    current: Formula | None = None,
) -> RecoverResult:
    """完整枚举并批量筛选所有 path、seg、seek 下标组合；单个 seed 不足以定解。"""
    current = current or Formula()
    parsed: list[tuple[bytes, list[str], str, float | None]] = []
    for item in samples:
        seed = item.get("seed_bytes")
        if seed is None:
            continue
        paths = list(item.get("paths") or [])
        official = str(item.get("hex") or "").strip()
        if len(paths) < 4 or not official:
            continue
        parsed.append((seed, paths, official, item.get("seek")))
    if parsed:
        latest_paths = parsed[-1][1]
        parsed = [item for item in parsed if item[1] == latest_paths]
    if len(parsed) < 2:
        return RecoverResult(status="needs_agent", reason="稳定反解至少需要两次不同 seed 的官方 HEX")
    directs: list[set[tuple[int, int, float]]] = []
    for seed, paths, official, seek in parsed:
        hits = {(hit["path"], hit["seg"], hit["seek"]) for hit in _direct_matches(paths, official, seek)}
        if not hits:
            return RecoverResult(status="needs_agent", reason="有抓包穷举不到 path/seg/seek")
        directs.append(hits)
    n = len(parsed[0][0])
    path_values = [{hit[0] for hit in hits} for hits in directs]
    seg_values = [{hit[1] for hit in hits} for hits in directs]
    seek_values = [{hit[2] for hit in hits} for hits in directs]
    path_idx = [
        i
        for i in range(n)
        if all(int(parsed[k][0][i]) % current.path_mod in path_values[k] for k in range(len(parsed)))
    ]
    seg_idx = [
        i
        for i in range(n)
        if all(int(parsed[k][0][i]) % current.seg_mod in seg_values[k] for k in range(len(parsed)))
    ]
    if not path_idx or not seg_idx:
        return RecoverResult(status="needs_agent", reason="两次抓包对不上同一套 path/seg 下标")
    seek_mod = current.seek_mod
    align = current.seek_align or 1
    seek_groups: dict[tuple[int, int, int], list[float]] = {}
    current_seeks = tuple(int(v) for v in current.seek_indices)
    for a, b, c in combinations_with_replacement(range(n), 3):
        values = []
        for k, (seed, _, _, _) in enumerate(parsed):
            product_v = (int(seed[a]) % seek_mod) * (int(seed[b]) % seek_mod) * (int(seed[c]) % seek_mod)
            value = go_round(product_v / align) * align
            if value not in seek_values[k]:
                break
            values.append(value)
        else:
            key = current_seeks if tuple(sorted(current_seeks)) == (a, b, c) else (a, b, c)
            seek_groups[key] = values
    if not seek_groups:
        return RecoverResult(status="needs_agent", reason="两次抓包对不上同一套 seek 下标")
    candidates: list[dict[str, Any]] = []
    for path_index, seg_index, seeks in product(path_idx, seg_idx, seek_groups):
        if all(
            (
                seed[path_index] % current.path_mod,
                seed[seg_index] % current.seg_mod,
                seek_groups[seeks][k],
            ) in directs[k]
            for k, (seed, _, _, _) in enumerate(parsed)
        ):
            candidates.append({"path_index": path_index, "seg_index": seg_index, "seek_indices": list(seeks)})
    if not candidates:
        return RecoverResult(status="needs_agent", reason="全部下标候选均被官方样本排除")
    if len(candidates) != 1:
        path_count = len({item["path_index"] for item in candidates})
        seg_count = len({item["seg_index"] for item in candidates})
        seek_count = len({tuple(item["seek_indices"]) for item in candidates})
        return RecoverResult(
            status="ambiguous",
            reason=(
                f"{len(parsed)} 次抓包后仍有 {path_count} 个 path、{seg_count} 个 seg、"
                f"{seek_count} 组 seek 下标，再 capture_page"
            ),
            candidates=candidates[:12],
            candidate_count=len(candidates),
        )
    candidate = candidates[0]
    formula = Formula(
        path_index=candidate["path_index"],
        path_mod=current.path_mod,
        seg_index=candidate["seg_index"],
        seg_mod=current.seg_mod,
        seek_indices=tuple(candidate["seek_indices"]),
        seek_mod=current.seek_mod,
        seek_align=current.seek_align,
        duration=current.duration,
        epoch=current.epoch,
        salt=current.salt,
        mark=current.mark,
    )
    for seed, paths, official, _ in parsed:
        if compute_hex(seed, paths, formula) != official:
            return RecoverResult(status="needs_agent", reason="稳定公式复算 HEX 不一致")
    status = "matched" if formula == current else "recovered"
    reason = (
        "当前公式是这几次抓包的唯一解"
        if status == "matched"
        else "多次抓包交叉后下标唯一，与官方 HEX 一致"
    )
    return RecoverResult(status=status, formula=formula, hex=parsed[0][2], reason=reason, candidates=candidates, candidate_count=1)


def _search_direct(
    seed: bytes,
    paths: Sequence[str],
    official_hex: str,
    seek_hint: float | None,
) -> dict[str, Any] | None:
    return next(_direct_matches(paths, official_hex, seek_hint), None)


def _direct_matches(
    paths: Sequence[str],
    official_hex: str,
    seek_hint: float | None,
) -> Iterator[dict[str, Any]]:
    """保留全部 HEX 等价的 path/seg/seek 命中，不用第一个命中排除其他正确候选。"""
    seeks: list[float]
    if seek_hint is not None:
        seeks = [float(seek_hint)]
    else:
        seeks = [float(v) for v in range(0, SEEK_MAX + 1, 10)]
    for path_i, path in enumerate(paths):
        segments = path_segments(path)
        limit = min(len(segments), 16)
        for seg_i in range(limit):
            for seek in seeks:
                try:
                    hex_value = hex_from_segment(segments[seg_i], seek, 4096.0)
                except ValueError:
                    continue
                if hex_value == official_hex:
                    yield {"path": path_i, "seg": seg_i, "seek": seek}


def _formula_matches_direct(seed: bytes, formula: Formula, direct: dict[str, Any]) -> bool:
    try:
        path_i = int(seed[formula.path_index]) % formula.path_mod
        seg_i = int(seed[formula.seg_index]) % formula.seg_mod
        return path_i == int(direct["path"]) and seg_i == int(direct["seg"]) and aligned_seek(seed, formula) == float(direct["seek"])
    except (ValueError, IndexError):
        return False


def _invert_indices(seed: bytes, current: Formula, direct: dict[str, Any]) -> Formula | None:
    path_i = int(direct["path"])
    seg_i = int(direct["seg"])
    seek = float(direct["seek"])
    default = Formula()
    if _formula_matches_direct(seed, default, direct):
        return default
    if _formula_matches_direct(seed, current, direct):
        return current
    path_bytes = [i for i in range(len(seed)) if int(seed[i]) % current.path_mod == path_i]
    seg_bytes = [i for i in range(len(seed)) if int(seed[i]) % current.seg_mod == seg_i]
    if default.path_index in path_bytes:
        path_bytes = [default.path_index] + [i for i in path_bytes if i != default.path_index]
    if default.seg_index in seg_bytes:
        seg_bytes = [default.seg_index] + [i for i in seg_bytes if i != default.seg_index]
    seek_mod = current.seek_mod
    align = current.seek_align or 1
    n = len(seed)
    path_index = path_bytes[0] if path_bytes else current.path_index
    seg_index = seg_bytes[0] if seg_bytes else current.seg_index
    if current.path_index in path_bytes:
        path_index = current.path_index
    if current.seg_index in seg_bytes:
        seg_index = current.seg_index
    seek_hit = None
    if _formula_matches_direct(
        seed,
        Formula(
            path_index=path_index,
            path_mod=current.path_mod,
            seg_index=seg_index,
            seg_mod=current.seg_mod,
            seek_indices=current.seek_indices,
            seek_mod=current.seek_mod,
            seek_align=current.seek_align,
            duration=current.duration,
            epoch=current.epoch,
            salt=current.salt,
            mark=current.mark,
        ),
        direct,
    ):
        seek_hit = current.seek_indices
    else:
        for a, b, c in product(range(n), repeat=3):
            product_v = (int(seed[a]) % seek_mod) * (int(seed[b]) % seek_mod) * (int(seed[c]) % seek_mod)
            if go_round(product_v / align) * align == seek:
                seek_hit = (a, b, c)
                break
    if seek_hit is None:
        return None
    return Formula(
        path_index=path_index,
        path_mod=current.path_mod,
        seg_index=seg_index,
        seg_mod=current.seg_mod,
        seek_indices=tuple(seek_hit),
        seek_mod=current.seek_mod,
        seek_align=current.seek_align,
        duration=current.duration,
        epoch=current.epoch,
        salt=current.salt,
        mark=current.mark,
    )
