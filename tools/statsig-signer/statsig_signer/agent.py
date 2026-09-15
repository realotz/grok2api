from __future__ import annotations

import ast
import json
import os
import re
import subprocess
import sys
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .algorithm import Formula, build_statsig, compute_hex, curves_hash, decode_seed, inspect_statsig_id
from .capture import capture
from .hermes import HermesKernel, Tool, grok2api_complete
from .recover import recover_formula, recover_formula_stable
from .runtime import HotRuntime, default_runtime
from .store import Pair, Store

SYSTEM = """你是 grok.com x-statsig-id 维修内核。
算法是可执行 JS：签名器常驻 Node，用 vm.eval 跑 hot/hex.js 里的 computeHex(seed, paths)。
不要改 Go。HEX 算法只许 write_hot_js，不要用 write_py 改 hex.js。
需要新鲜对照时自己 capture_page，钩 digest（官方 HEX）和 animate/getAnimations（seek）。
70 字节壳仍由签名器 Python 套（epoch 1682924400，salt obfiowerehiring，末字节 0x03）。
步骤：capture_page → recover_indices。返回 hex_js 就立刻 write_hot_js(source=hex_js 原文)。
write 失败若仍返回 hex_js，立刻再 write。ambiguous 就再 capture_page 后 recover。
工具链坏了（抓包、eval、指纹、重试）可以 read_py / write_py 改 statsig_signer/*.py、tests/*.py、hot/prelude.js。语法错或单测失败会回滚。当前进程里已绑定的函数要等下次 watch tick 才加载。
write 成功后 verify_signature（会再抓一页）。新鲜页对不上不能 exit。
verify 抓包失败就再 verify 或 capture_page，禁止连续 read_py / fetch_chunk 空转。
"""


def update(
    store: Store | None = None,
    browser: str = "local",
    use_hermes: bool = True,
    force_hermes: bool = False,
    fixture: dict[str, Any] | None = None,
    runtime: HotRuntime | None = None,
    defer_capture: bool = False,
    capture_fn: Any = None,
    **capture_kwargs: Any,
) -> dict[str, Any]:
    store = store or Store()
    runtime = runtime or default_runtime()
    do_capture = capture_fn or capture
    if defer_capture and fixture is None:
        captured = {"ok": False, "browser": browser, "paths": [], "script_urls": [], "chunks": []}
    else:
        captured = fixture or do_capture(browser=browser, **capture_kwargs)
        if not captured.get("ok") and not (captured.get("seed") and captured.get("hex") and captured.get("paths")):
            return {"ok": False, "stage": "capture", "capture": _public_capture(captured)}
        captured["ok"] = True
    if not (captured.get("seed") and captured.get("hex") and captured.get("paths")):
        kernel = _kernel(store, captured, runtime, do_capture, capture_kwargs)
        prompt = json.dumps(
            {
                "task": "先 capture_page 抓同一页 seed/官方 HEX/curves，再修复 computeHex，verify_signature 通过才结束。",
                "current_formula": store.formula.to_dict(),
                "hot_js": runtime.current_js()[:4000],
            },
            ensure_ascii=False,
        )
        result = kernel.run(SYSTEM, prompt)
        return _finish_hermes(store, captured, runtime, result)
    pair = _pair_from_capture(captured)
    seed = decode_seed(pair.seed)
    recovered = recover_formula(
        seed,
        pair.paths,
        pair.hex,
        store.formula,
        _seek_hint(captured),
    )
    if recovered.status == "matched" and recovered.formula is not None and not force_hermes:
        store.save_pair(pair)
        accepted = _accept(runtime, pair, recovered.formula)
        return {
            "ok": accepted.get("ok", False),
            "stage": recovered.status,
            "reason": recovered.reason,
            "formula": recovered.formula.to_dict(),
            "pair": {"hex": pair.hex, "curves_hash": pair.curves_hash, "source": pair.source},
            "accepted": accepted,
        }
    if recovered.status == "recovered" and recovered.formula is not None and fixture is not None and not force_hermes:
        store.save_formula(recovered.formula)
        runtime.write_js(_hex_js_for_formula(recovered.formula, runtime))
        store.save_pair(pair)
        accepted = _accept(runtime, pair, recovered.formula)
        return {
            "ok": accepted.get("ok", False),
            "stage": recovered.status,
            "reason": recovered.reason,
            "formula": recovered.formula.to_dict(),
            "pair": {"hex": pair.hex, "curves_hash": pair.curves_hash, "source": pair.source},
            "accepted": accepted,
        }
    if not use_hermes:
        return {
            "ok": False,
            "stage": "needs_agent",
            "reason": recovered.reason,
            "capture": _public_capture(captured),
        }
    kernel = _kernel(store, captured, runtime, do_capture, capture_kwargs)
    prompt = json.dumps(
        {
            "task": "当前公式算不出官方 HEX。可重新 capture_page，或 recover_indices / 改 computeHex。verify_signature 通过才结束。",
            "reason": recovered.reason,
            "current_formula": store.formula.to_dict(),
            "official_hex": captured.get("hex"),
            "capture": _public_capture(captured),
            "chunk_urls": (captured.get("script_urls") or captured.get("chunks") or [])[:12],
        },
        ensure_ascii=False,
    )
    result = kernel.run(SYSTEM, prompt)
    return _finish_hermes(store, captured, runtime, result)


def fixture_from_pair_file(path: Path) -> dict[str, Any]:
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    paths = list(data.get("paths") or [])
    return {
        "ok": True,
        "seed": data.get("seed"),
        "hex": data.get("hex"),
        "paths": paths,
        "curves_hash": data.get("curves_hash") or curves_hash(paths),
        "seeks": [{"value": (data.get("fingerprints") or {}).get("seek")}] if (data.get("fingerprints") or {}).get("seek") is not None else [{"value": 240}],
        "source": data.get("source") or str(path),
        "script_urls": [],
        "chunks": [],
        "browser": "fixture",
    }


def _finish_hermes(store: Store, captured: dict[str, Any], runtime: HotRuntime, result: Any) -> dict[str, Any]:
    pair_info: dict[str, Any] = {}
    accepted: dict[str, Any] = {"ok": False, "error": "还没有可用抓包"}
    if captured.get("seed") and captured.get("hex") and captured.get("paths"):
        pair = _pair_from_capture(captured)
        accepted = _accept(runtime, pair, store.formula)
        pair_info = {"hex": pair.hex, "curves_hash": pair.curves_hash, "source": pair.source}
    return {
        "ok": accepted.get("ok", False),
        "stage": "hermes",
        "content": result.content,
        "turns": result.turns,
        "stopped": getattr(result, "stopped", ""),
        "formula": store.formula.to_dict(),
        "pair": pair_info,
        "capture": _public_capture(captured),
        "accepted": accepted,
    }


def _kernel(
    store: Store,
    captured: dict[str, Any],
    runtime: HotRuntime,
    capture_fn: Any,
    capture_kwargs: dict[str, Any] | None,
) -> HermesKernel:
    base = os.environ.get("GROK2API_BASE", "http://127.0.0.1:18000/v1")
    key = os.environ.get("GROK2API_KEY", "").strip()
    model = os.environ.get("GROK2API_MODEL", "grok-4.6")
    if not key:
        raise RuntimeError("Hermes 需要 GROK2API_KEY 指向 grok2api 客户端密钥")
    extra = dict(capture_kwargs or {})
    samples: list[dict[str, Any]] = []
    idle = {"n": 0}

    def _progress() -> None:
        idle["n"] = 0

    def _guard_idle(name: str, fn: Any) -> Any:
        def wrapped(**kwargs: Any) -> Any:
            idle["n"] += 1
            if idle["n"] >= 3:
                nxt = "verify_signature" if captured.get("seed") and captured.get("hex") else "capture_page"
                return {
                    "ok": False,
                    "error": f"{name} 空转已打断",
                    "next": nxt,
                    "note": "不要再 read_py/fetch_chunk，去 capture_page 或 verify_signature。",
                }
            return fn(**kwargs)

        return wrapped

    def _need_capture() -> dict[str, Any] | None:
        if captured.get("seed") and captured.get("hex") and captured.get("paths"):
            return None
        return {"error": "先调用 capture_page 抓同一页 seed/HEX/curves"}

    def _remember(result: dict[str, Any] | None) -> None:
        if not result or not (result.get("seed") and result.get("hex") and result.get("paths")):
            return
        try:
            seed_bytes = decode_seed(result["seed"])
        except Exception:
            return
        sample = {
            "seed_bytes": seed_bytes,
            "paths": list(result["paths"]),
            "hex": result["hex"],
            "seek": _seek_hint(result),
            "url": result.get("url"),
        }
        for existing in samples:
            if existing["seed_bytes"] == sample["seed_bytes"] and existing["hex"] == sample["hex"]:
                return
        samples.append(sample)

    def capture_page(browser: str = "local", url: str = "https://grok.com/imagine", headed: bool = False) -> dict[str, Any]:
        kwargs = dict(extra)
        kwargs.update({"url": url, "headed": headed})
        if browser == "x2api" and extra.get("addr"):
            kwargs["addr"] = extra["addr"]
        try:
            result = capture_fn(browser=browser, **kwargs)
        except Exception as exc:
            if browser == "x2api":
                result = capture_fn(browser="local", **{k: v for k, v in kwargs.items() if k != "addr"})
                result = result or {}
                result["fallback"] = f"x2api 失败已改 local: {exc}"
            else:
                raise
        captured.clear()
        captured.update(result or {})
        _progress()
        _remember(captured)
        public = _public_capture(captured)
        if captured.get("ok"):
            public["hex"] = captured.get("hex")
            public["official_hex"] = captured.get("hex")
            public["chunk_urls"] = (captured.get("script_urls") or captured.get("chunks") or [])[:12]
            public["signer_chunks"] = [
                {"url": item.get("url"), "hits": item.get("hits")}
                for item in (captured.get("signer_chunks") or [])
            ]
            public["sample_count"] = len(samples)
        return public

    def inspect_capture() -> dict[str, Any]:
        missing = _need_capture()
        if missing:
            return missing
        seed = decode_seed(captured["seed"])
        anims = captured.get("anims") or []
        keyframes = ((anims[0] or {}).get("keyframes") if anims else []) or []
        return {
            "official_hex": captured.get("hex"),
            "hex_from_tostring": captured.get("hex_from_tostring") or "",
            "hex_agree": bool(captured.get("hex_agree")),
            "hex_len": len(captured.get("hex") or ""),
            "salt": captured.get("salt"),
            "prefix": captured.get("prefix") or "",
            "digest_raw": (captured.get("digest_raw") or "")[:240],
            "seek": _seek_hint(captured),
            "sentry_release": captured.get("sentry_release") or "",
            "seed_bytes": list(seed),
            "path_count": len(captured.get("paths") or []),
            "keyframes": [
                {"color": item.get("color"), "transform": item.get("transform")}
                for item in keyframes[:4]
                if isinstance(item, dict)
            ],
            "signer_chunks": captured.get("signer_chunks") or [],
            "chunk_count": len(captured.get("chunks") or captured.get("script_urls") or []),
        }

    def find_signer_chunk() -> dict[str, Any]:
        existing = captured.get("signer_chunks") or []
        if existing:
            return {"ok": True, "chunks": existing}
        urls = list(captured.get("chunks") or []) + list(captured.get("script_urls") or [])
        from .capture import discover_signer_chunks

        found = discover_signer_chunks(urls)
        captured["signer_chunks"] = found
        if not found:
            return {
                "ok": False,
                "chunks": [],
                "next": "recover_indices",
                "note": "明文 salt chunk 找不到是正常的。不要无界 fetch_chunk，对 recover_indices 的 hex_js 调用 write_hot_js。",
            }
        return {"ok": True, "chunks": found}

    def recover() -> dict[str, Any]:
        missing = _need_capture()
        if missing:
            return missing
        _progress()
        _remember(captured)
        if len(samples) >= 2:
            result = recover_formula_stable(samples, store.formula)
        else:
            result = recover_formula(
                decode_seed(captured["seed"]),
                captured["paths"],
                captured["hex"],
                store.formula,
                _seek_hint(captured),
            )
        payload: dict[str, Any] = {
            "status": result.status,
            "reason": result.reason,
            "candidates": result.candidates,
            "sample_count": len(samples),
        }
        if result.formula:
            payload["formula"] = result.formula.to_dict()
            payload["hex_js"] = _hex_js_for_formula(result.formula, runtime)
            payload["next"] = "write_hot_js"
            payload["note"] = "把 hex_js 原样交给 write_hot_js。第二页由该工具内部验证，不要再 capture_page。"
        elif result.status == "ambiguous":
            payload["next"] = "capture_page"
            payload["note"] = "下标还不唯一。换 / 或 /imagine 再 capture_page，然后 recover_indices，不要 fetch_chunk。"
        return payload

    def fetch_chunk(url: str) -> dict[str, Any]:
        if not url.startswith("https://cdn.grok.com/"):
            return {"error": "只允许 cdn.grok.com chunk", "next": "write_hot_js"}
        request = urllib.request.Request(
            url,
            headers={
                "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
                "Referer": "https://grok.com/",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                text = response.read()[:120_000].decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            return {
                "ok": False,
                "url": url,
                "error": f"HTTP {exc.code}",
                "next": "write_hot_js",
                "note": "这个 URL 不用再拉。有 hex_js 就 write_hot_js。",
            }
        needles = ["obfiowerehiring", "animate", "4096", "x-statsig-id", "getComputedStyle"]
        hits = [item for item in needles if item in text]
        snippet = ""
        for item in ("obfiowerehiring", "W[", "animate"):
            idx = text.find(item)
            if idx >= 0:
                snippet = text[max(0, idx - 200) : idx + 400]
                break
        return {"url": url, "hits": hits, "snippet": snippet, "size": len(text)}

    def eval_hot_js(source: str) -> dict[str, Any]:
        _progress()
        missing = _need_capture()
        if missing:
            return missing
        seed = decode_seed(captured["seed"])
        hex_value = runtime.eval_js(source, seed, list(captured["paths"]))
        return {"hex": hex_value, "official": captured["hex"], "match": hex_value == captured["hex"]}

    def write_hot_js(source: str) -> dict[str, Any]:
        _progress()
        check = eval_hot_js(source)
        if check.get("error"):
            return check
        if not check["match"]:
            return {"ok": False, "error": "eval HEX 不等于官方 HEX，拒绝写入", **check}
        _remember(captured)
        if captured.get("browser") != "fixture" and len(samples) >= 2:
            unique = recover_formula_stable(samples, store.formula)
            if unique.formula:
                matched_all = True
                for item in samples:
                    try:
                        got = runtime.eval_js(source, item["seed_bytes"], list(item["paths"]))
                    except Exception:
                        matched_all = False
                        break
                    if got != item.get("hex"):
                        matched_all = False
                        break
                if matched_all:
                    store.save_formula(unique.formula)
                    path = runtime.write_js(source)
                    store.save_pair(_pair_from_capture(captured))
                    return {"ok": True, "path": str(path), "hex": check["hex"], "samples": len(samples)}
        if captured.get("browser") != "fixture":
            first_url = str(captured.get("url") or extra.get("url") or "https://grok.com/imagine")
            second_url = "https://grok.com/" if "imagine" in first_url else "https://grok.com/imagine"
            print(f"write_hot_js second-page capture {second_url}", file=sys.stderr, flush=True)
            second: dict[str, Any] = {}
            used_url = second_url
            for attempt, used_url in enumerate((second_url, second_url, first_url, first_url)):
                second = capture_fn(
                    browser=str(captured.get("browser") or extra.get("browser") or "local"),
                    url=used_url,
                    **{k: v for k, v in extra.items() if k not in ("url", "headed", "browser")},
                ) or {}
                if second.get("seed") and second.get("hex") and second.get("paths"):
                    break
                print(f"write_hot_js second-page retry {attempt + 1} {used_url}", file=sys.stderr, flush=True)
            if not (second.get("seed") and second.get("hex") and second.get("paths")):
                return {"ok": False, "error": "第二页抓包失败，拒绝写入单次 HEX", "next": "capture_page"}
            _remember(captured)
            _remember(second)
            try:
                second_hex = runtime.eval_js(source, decode_seed(second["seed"]), list(second["paths"]))
            except Exception as exc:
                return {"ok": False, "error": f"第二页 eval 失败: {exc}"}
            if second_hex != second.get("hex"):
                payload: dict[str, Any] = {
                    "ok": False,
                    "error": "第二页官方 HEX 不匹配，拒绝写入单次反解",
                    "first": check,
                    "second": {"official": second.get("hex"), "hex": second_hex, "url": second_url},
                    "sample_count": len(samples),
                    "status": "ambiguous",
                    "next": "capture_page",
                }
                stable = recover_formula_stable(samples, store.formula)
                payload["reason"] = stable.reason
                if stable.formula:
                    payload["hex_js"] = _hex_js_for_formula(stable.formula, runtime)
                    payload["formula"] = stable.formula.to_dict()
                    payload["status"] = stable.status
                    payload["next"] = "write_hot_js"
                    payload["note"] = "下标已唯一。把 hex_js 原样再 write_hot_js，不要 fetch_chunk。"
                else:
                    payload["status"] = stable.status
                    payload["candidates"] = stable.candidates[:8]
                    payload["note"] = stable.reason or "再 capture_page 后 recover_indices，不要 fetch_chunk。"
                return payload
            if len(samples) >= 2:
                unique = recover_formula_stable(samples, store.formula)
                if not unique.formula:
                    return {
                        "ok": False,
                        "error": "两页 HEX 能对上，但下标还不唯一，拒绝写入",
                        "reason": unique.reason,
                        "status": unique.status,
                        "sample_count": len(samples),
                        "candidates": unique.candidates[:8],
                        "next": "capture_page",
                        "note": "换 / 或 /imagine 再 capture_page，然后 recover_indices。",
                    }
                store.save_formula(unique.formula)
        path = runtime.write_js(source)
        store.save_pair(_pair_from_capture(captured))
        return {"ok": True, "path": str(path), "hex": check["hex"]}

    def apply_pair() -> dict[str, Any]:
        missing = _need_capture()
        if missing:
            return missing
        seed = decode_seed(captured["seed"])
        hex_value = runtime.compute(seed, list(captured["paths"]), store.formula)
        if hex_value != captured["hex"]:
            return {"ok": False, "error": "当前热代码 HEX 不等于官方 HEX，不能只换 pair"}
        store.save_pair(_pair_from_capture(captured))
        return {"ok": True, "hex": captured["hex"]}

    def _fresh_page_check() -> dict[str, Any] | None:
        if captured.get("browser") == "fixture":
            return None
        first_url = str(captured.get("url") or extra.get("url") or "https://grok.com/imagine")
        fresh_url = "https://grok.com/" if "imagine" in first_url else "https://grok.com/imagine"
        print(f"verify_signature fresh capture {fresh_url}", file=sys.stderr, flush=True)
        fresh: dict[str, Any] = {}
        used_url = fresh_url
        for attempt, used_url in enumerate((fresh_url, fresh_url, first_url, first_url)):
            fresh = capture_fn(
                browser=str(captured.get("browser") or extra.get("browser") or "local"),
                url=used_url,
                **{k: v for k, v in extra.items() if k not in ("url", "headed", "browser")},
            ) or {}
            if fresh.get("seed") and fresh.get("hex") and fresh.get("paths"):
                break
            print(f"verify_signature fresh retry {attempt + 1} {used_url}", file=sys.stderr, flush=True)
        if not (fresh.get("seed") and fresh.get("hex") and fresh.get("paths")):
            return {"ok": False, "exit": False, "error": "验签新鲜抓包失败", "next": "capture_page"}
        _remember(fresh)
        try:
            hot_hex = runtime.eval_js(runtime.current_js(), decode_seed(fresh["seed"]), list(fresh["paths"]))
        except Exception as exc:
            return {"ok": False, "exit": False, "error": f"新鲜页 eval 失败: {exc}"}
        if hot_hex == fresh.get("hex"):
            return _accept(runtime, _pair_from_capture(fresh), store.formula)
        payload: dict[str, Any] = {
            "ok": False,
            "exit": False,
            "error": "新鲜页官方 HEX 不匹配，不能退出",
            "fresh": {"url": fresh_url, "official": fresh.get("hex"), "hex": hot_hex},
            "sample_count": len(samples),
            "next": "capture_page",
        }
        if len(samples) >= 2:
            stable = recover_formula_stable(samples, store.formula)
            payload["status"] = stable.status
            payload["reason"] = stable.reason
            if stable.formula:
                payload["hex_js"] = _hex_js_for_formula(stable.formula, runtime)
                payload["formula"] = stable.formula.to_dict()
                payload["next"] = "write_hot_js"
                payload["note"] = "把 hex_js 原样 write_hot_js，不要 fetch_chunk。"
            else:
                payload["candidates"] = stable.candidates[:8]
                payload["note"] = stable.reason or "再 capture_page 后 recover_indices。"
        else:
            payload["note"] = "再 capture_page 后 recover_indices，不要 fetch_chunk。"
        return payload

    def verify_signature() -> dict[str, Any]:
        _progress()
        missing = _need_capture()
        if missing:
            return missing
        accepted = _accept(runtime, _pair_from_capture(captured), store.formula)
        if not accepted.get("ok"):
            accepted["exit"] = False
            accepted["next"] = "recover_indices"
            return accepted
        fresh = _fresh_page_check()
        if fresh is not None:
            if not fresh.get("ok"):
                return fresh
            accepted = fresh
        accepted["exit"] = True
        accepted["reason"] = "verify_signature 通过（含新鲜页）"
        return accepted

    def exit_repair(reason: str = "") -> dict[str, Any]:
        accepted = verify_signature()
        if not accepted.get("ok"):
            return {
                "ok": False,
                "exit": False,
                "error": accepted.get("error") or "验签未通过，不能退出",
                "accepted": accepted,
            }
        return {"ok": True, "exit": True, "reason": reason or accepted.get("reason") or "verify_signature 通过"}

    tools = [
        Tool(
            "capture_page",
            "打开 grok.com/imagine。官方 HEX 来自 crypto.subtle.digest 里 salt 后的明文，并用 Number#toString(16) 交叉。animate(4096)/getAnimations 只提供 seek 提示。browser=local 或 x2api。",
            {
                "type": "object",
                "properties": {
                    "browser": {"type": "string", "enum": ["local", "x2api"]},
                    "url": {"type": "string"},
                    "headed": {"type": "boolean"},
                },
            },
            capture_page,
        ),
        Tool("read_hot_js", "读取签名器正在 eval 的 hot/hex.js", {"type": "object", "properties": {}}, lambda: {"source": runtime.current_js()}),
        Tool(
            "eval_hot_js",
            "用 Node vm.eval 跑一段 computeHex JS，和官方 HEX 比较。不写盘。",
            {"type": "object", "properties": {"source": {"type": "string"}}, "required": ["source"]},
            eval_hot_js,
        ),
        Tool(
            "write_hot_js",
            "把 recover_indices 返回的 hex_js 原样写入。内部再抓一页对照；两页对上且下标唯一才落盘。成功后再 verify_signature。",
            {"type": "object", "properties": {"source": {"type": "string"}}, "required": ["source"]},
            write_hot_js,
        ),
        Tool("inspect_capture", "看抓包：seed 字节、digest 明文、seek、keyframes、签名 chunk", {"type": "object", "properties": {}}, inspect_capture),
        Tool("find_signer_chunk", "在已加载 chunk 里搜 obfiowerehiring/animate(4096) 签名模块", {"type": "object", "properties": {}}, find_signer_chunk),
        Tool("recover_indices", "穷举 seed 下标，看能否对上官方 HEX。只覆盖当前 SVG HEX 公式。", {"type": "object", "properties": {}}, recover),
        Tool(
            "fetch_chunk",
            "拉取 grok CDN chunk，抽取 obfiowerehiring/W[n]/animate 附近源码",
            {"type": "object", "properties": {"url": {"type": "string"}}, "required": ["url"]},
            _guard_idle("fetch_chunk", fetch_chunk),
        ),
        Tool("apply_pair", "热代码已匹配官方 HEX 时，只更新 seed/curves/官方 HEX", {"type": "object", "properties": {}}, apply_pair),
        Tool(
            "verify_signature",
            "先核写入时那页，再自己 capture 另一页新鲜对照。两页 HEX 和 70 字节壳都对才接受并退出。新鲜页不对不能 exit。",
            {"type": "object", "properties": {}},
            verify_signature,
        ),
        Tool(
            "exit",
            "验签通过后结束任务。未通过会被拒绝并继续。",
            {"type": "object", "properties": {"reason": {"type": "string"}}},
            exit_repair,
        ),
        Tool(
            "read_py",
            "读签名器仓库里的 Python/prelude。只限 statsig_signer/*.py、tests/*.py、hot/prelude.js。",
            {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
            _guard_idle("read_py", lambda path: read_signer_file(path)),
        ),
        Tool(
            "write_py",
            "改签名器 Python 或 prelude。不能改 Go、hex.js、data。写完跑单测，失败回滚。当前这次工具闭包不会热替换。",
            {
                "type": "object",
                "properties": {"path": {"type": "string"}, "source": {"type": "string"}},
                "required": ["path", "source"],
            },
            _guard_idle("write_py", lambda path, source: write_signer_file(path, source)),
        ),
    ]
    max_turns = int(os.environ.get("STATSIG_AGENT_MAX_TURNS") or "200")
    return HermesKernel(grok2api_complete(base, key, model, timeout=180), tools, max_turns=max_turns)


def _accept(runtime: HotRuntime, pair: Pair, formula: Formula) -> dict[str, Any]:
    seed = pair.seed_bytes()
    try:
        hot_hex = runtime.compute(seed, pair.paths, formula)
    except Exception as exc:
        return {"ok": False, "error": f"eval 失败: {exc}"}
    if hot_hex != pair.hex:
        return {"ok": False, "error": "热代码 HEX 不等于官方 HEX", "hex": hot_hex, "official": pair.hex}
    signature = build_statsig(seed, hot_hex, "POST", "/rest/app-chat/conversations/new", 1757419200, formula)
    info = inspect_statsig_id(signature, formula)
    if decode_seed(info["seed"]) != seed:
        return {"ok": False, "error": "签名壳 seed 对不上"}
    if info["mark"] != 3:
        return {"ok": False, "error": f"签名壳 mark={info['mark']}"}
    return {"ok": True, "hex": hot_hex, "statsig_id": signature, "mark": info["mark"]}


_COMPUTE_HEX = re.compile(r"function computeHex\(seed, paths\) \{.*?\n\}", re.S)
_SIGNER_ROOT = Path(__file__).resolve().parents[1]
_PRELUDE_PATH = _SIGNER_ROOT / "hot" / "prelude.js"


def _safe_signer_path(rel: str) -> Path:
    rel = (rel or "").replace("\\", "/").strip().lstrip("/")
    if not rel or ".." in Path(rel).parts:
        raise ValueError("非法路径")
    allowed = (
        (rel.startswith("statsig_signer/") and rel.endswith(".py"))
        or (rel.startswith("tests/") and rel.endswith(".py"))
        or rel == "hot/prelude.js"
    )
    if rel == "hot/hex.js" or rel.startswith("data/") or not allowed:
        raise ValueError("只允许 statsig_signer/*.py、tests/*.py、hot/prelude.js")
    path = (_SIGNER_ROOT / rel).resolve()
    root = _SIGNER_ROOT.resolve()
    if path != root and root not in path.parents:
        raise ValueError("路径越界")
    return path


def read_signer_file(rel: str) -> dict[str, Any]:
    try:
        path = _safe_signer_path(rel)
    except ValueError as exc:
        return {"ok": False, "error": str(exc)}
    if not path.exists():
        return {"ok": False, "error": "文件不存在", "path": rel}
    text = path.read_text(encoding="utf-8")
    return {"ok": True, "path": rel, "source": text, "bytes": len(text.encode("utf-8"))}


def write_signer_file(rel: str, source: str, run_tests: bool = True) -> dict[str, Any]:
    try:
        path = _safe_signer_path(rel)
    except ValueError as exc:
        return {"ok": False, "error": str(exc)}
    raw = source if isinstance(source, str) else str(source)
    if len(raw.encode("utf-8")) > 200_000:
        return {"ok": False, "error": "文件太大"}
    if path.suffix == ".py":
        try:
            ast.parse(raw)
        except SyntaxError as exc:
            return {"ok": False, "error": f"语法错误: {exc}"}
    backup = path.read_text(encoding="utf-8") if path.exists() else None
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(raw if raw.endswith("\n") else raw + "\n", encoding="utf-8")
    if run_tests:
        env = os.environ.copy()
        env["PYTHONPATH"] = str(_SIGNER_ROOT) + os.pathsep + env.get("PYTHONPATH", "")
        try:
            proc = subprocess.run(
                [sys.executable, "-m", "unittest", "discover", "-s", "tests", "-q"],
                cwd=str(_SIGNER_ROOT),
                env=env,
                capture_output=True,
                text=True,
                timeout=90,
            )
        except Exception as exc:
            if backup is None:
                path.unlink(missing_ok=True)
            else:
                path.write_text(backup, encoding="utf-8")
            return {"ok": False, "error": f"测试没跑成，已回滚: {exc}"}
        if proc.returncode != 0:
            if backup is None:
                path.unlink(missing_ok=True)
            else:
                path.write_text(backup, encoding="utf-8")
            return {
                "ok": False,
                "error": "测试失败，已回滚",
                "stderr": (proc.stderr or proc.stdout or "")[-1500:],
            }
    return {
        "ok": True,
        "path": rel,
        "bytes": path.stat().st_size,
        "note": "已写入。当前这次工具闭包不会热替换，下次 watch tick 会加载。",
    }


def _confirm_or_stabilize(
    do_capture: Any,
    browser: str,
    capture_kwargs: dict[str, Any],
    first: dict[str, Any],
    current: Formula,
    recovered: Any,
) -> Any:
    first_url = str(first.get("url") or capture_kwargs.get("url") or "https://grok.com/imagine")
    second_url = "https://grok.com/" if "imagine" in first_url else "https://grok.com/imagine"
    kwargs = dict(capture_kwargs or {})
    kwargs["url"] = second_url
    second = do_capture(browser=browser, **kwargs)
    if not (second.get("seed") and second.get("hex") and second.get("paths")):
        recovered.status = "needs_agent"
        recovered.reason = "第二页抓包失败，拒绝写入单次反解下标"
        recovered.formula = None
        return recovered
    try:
        second_hex = compute_hex(decode_seed(second["seed"]), list(second["paths"]), recovered.formula)
    except Exception:
        second_hex = ""
    if second_hex == second.get("hex"):
        recovered.reason = "第二页官方 HEX 也匹配，接受公式"
        return recovered
    stable = recover_formula_stable(
        [
            {
                "seed_bytes": decode_seed(first["seed"]),
                "paths": first.get("paths"),
                "hex": first.get("hex"),
                "seek": _seek_hint(first),
            },
            {
                "seed_bytes": decode_seed(second["seed"]),
                "paths": second.get("paths"),
                "hex": second.get("hex"),
                "seek": _seek_hint(second),
            },
        ],
        current,
    )
    return stable


def _hex_js_for_formula(formula: Formula, runtime: HotRuntime | None = None) -> str:
    seeks = list(formula.seek_indices) + [0, 0, 0]
    body = (
        "function computeHex(seed, paths) {\n"
        f"  const pathIndex = seed[{formula.path_index}] % {formula.path_mod};\n"
        "  const path = paths[pathIndex];\n"
        "  const segments = pathSegments(path);\n"
        f"  const segIdx = seed[{formula.seg_index}] % {formula.seg_mod};\n"
        "  const seek = Math.round((("
        f"seed[{seeks[0]}] % {formula.seek_mod}) * "
        f"(seed[{seeks[1]}] % {formula.seek_mod}) * "
        f"(seed[{seeks[2]}] % {formula.seek_mod})) / 10) * 10;\n"
        f"  return hexFromSegment(segments[segIdx], seek, {int(formula.duration)});\n"
        "}"
    )
    prelude = ""
    if _PRELUDE_PATH.exists():
        prelude = _PRELUDE_PATH.read_text(encoding="utf-8")
    else:
        path = (runtime.js_path if runtime is not None else None) or (Path(__file__).resolve().parents[1] / "hot" / "hex.js")
        source = path.read_text(encoding="utf-8") if path.exists() else ""
        if "function pathSegments" in source:
            prelude = _COMPUTE_HEX.sub("", source, count=1)
    return (prelude.rstrip() + "\n\n" + body + "\n") if prelude.strip() else body + "\n"


def _pair_from_capture(captured: dict[str, Any]) -> Pair:
    paths = list(captured.get("paths") or [])
    return Pair(
        seed=str(captured.get("seed") or ""),
        hex=str(captured.get("hex") or ""),
        paths=paths[:4],
        source=str(captured.get("source") or captured.get("url") or "live capture"),
        curves_hash=str(captured.get("curves_hash") or curves_hash(paths[:4])),
        updated_at=datetime.now(timezone.utc).isoformat(),
        fingerprints={
            "sentry_release": captured.get("sentry_release") or "",
            "seek": _seek_hint(captured),
            "salt": captured.get("salt") or "",
        },
    )


def _seek_hint(captured: dict[str, Any]) -> float | None:
    seeks = captured.get("seeks") or []
    if seeks and isinstance(seeks[0], dict) and seeks[0].get("value") is not None:
        try:
            return float(seeks[0]["value"])
        except (TypeError, ValueError):
            return None
    return None


def _public_capture(captured: dict[str, Any]) -> dict[str, Any]:
    return {
        "ok": captured.get("ok"),
        "browser": captured.get("browser"),
        "hex_len": len(captured.get("hex") or ""),
        "seed_len": len(captured.get("seed") or ""),
        "path_count": len(captured.get("paths") or []),
        "curves_hash": captured.get("curves_hash"),
        "sentry_release": captured.get("sentry_release"),
        "salt": captured.get("salt"),
        "seek": _seek_hint(captured),
        "prefix": captured.get("prefix") or "",
    }
