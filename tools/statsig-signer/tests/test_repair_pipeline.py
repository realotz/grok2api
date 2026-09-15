from __future__ import annotations

import json
import shutil
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory

import os
from unittest.mock import patch

from statsig_signer.agent import _hex_js_for_formula, _kernel, fixture_from_pair_file, update
from statsig_signer.algorithm import Formula, compute_hex, decode_seed, encode_raw_std, inspect_statsig_id, valid_statsig_id
from statsig_signer.recover import recover_formula
from statsig_signer.runtime import HotRuntime
from statsig_signer.store import Pair, Store

ROOT = Path(__file__).resolve().parents[1]
PAIR_PATH = ROOT / "data" / "pair.json"
HOOK_DEBUG = Path(
    "/Users/real/Documents/code/grok2api/backend/internal/infra/provider/web/testdata/statsig_live_pair.debug.json"
)
CANONICAL_JS = ROOT / "hot" / "hex.js"
WORKER_JS = ROOT / "hot" / "eval_worker.js"


class RepairPipelineTest(unittest.TestCase):
    def test_hook_capture_has_same_page_seed_hex_paths_and_seek(self) -> None:
        pair = json.loads(PAIR_PATH.read_text(encoding="utf-8"))
        debug = json.loads(HOOK_DEBUG.read_text(encoding="utf-8"))
        self.assertEqual(len(decode_seed(pair["seed"])), 48)
        self.assertEqual(pair["hex"], "1a1860fae147ae147ae02e147ae147ae1402e147ae147ae140fae147ae147ae00")
        self.assertEqual(len(pair["paths"]), 4)
        for path in pair["paths"]:
            self.assertTrue(path.startswith("M 10,30 C"))
            self.assertGreaterEqual(path.count("C"), 4)
        self.assertEqual(debug["hex_len"], 65)
        self.assertEqual(debug["path_count"], 4)
        self.assertEqual(debug["seeks"][0]["value"], 240)
        self.assertEqual(debug["anims"][0]["duration"], 4096)
        self.assertIn("rgb(16, 153, 135)", debug["anims"][0]["keyframes"][0]["color"])
        self.assertIn("rotate(169deg)", debug["anims"][0]["keyframes"][1]["transform"])

    def test_agent_repairs_wrong_hex_js_then_accepts_signature(self) -> None:
        fixture = fixture_from_pair_file(PAIR_PATH)
        with TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            hot = tmp_path / "hot"
            data = tmp_path / "data"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            sabotaged = CANONICAL_JS.read_text(encoding="utf-8").replace("seed[5] % 4", "seed[0] % 4", 1)
            (hot / "hex.js").write_text(sabotaged, encoding="utf-8")
            store = Store(data)
            store.save_formula(Formula(path_index=0, seg_index=1, seek_indices=(0, 1, 2)))
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            before = runtime.compute(decode_seed(fixture["seed"]), fixture["paths"])
            self.assertNotEqual(before, fixture["hex"])

            result = update(
                store=store,
                fixture=fixture,
                runtime=runtime,
                use_hermes=False,
            )
            self.assertTrue(result.get("ok"), result)
            self.assertEqual(result.get("stage"), "recovered")
            accepted = result.get("accepted") or {}
            self.assertTrue(accepted.get("ok"), accepted)
            self.assertEqual(accepted.get("hex"), fixture["hex"])
            self.assertTrue(valid_statsig_id(accepted.get("statsig_id") or ""))
            info = inspect_statsig_id(accepted["statsig_id"])
            self.assertEqual(decode_seed(info["seed"]), decode_seed(fixture["seed"]))
            self.assertEqual(info["mark"], 3)

            written = (hot / "hex.js").read_text(encoding="utf-8")
            self.assertIn("seed[5] % 4", written)
            self.assertIn("seed[39] % 16", written)
            after = runtime.compute(decode_seed(fixture["seed"]), fixture["paths"])
            self.assertEqual(after, fixture["hex"])
            self.assertEqual(store.formula.path_index, 5)
            self.assertEqual(store.formula.seg_index, 39)
            self.assertEqual(tuple(store.formula.seek_indices), (3, 31, 36))
            self.assertEqual(store.pair.hex, fixture["hex"])

    def test_agent_capture_page_tool_then_repair_and_accept(self) -> None:
        fixture = fixture_from_pair_file(PAIR_PATH)
        calls = {"n": 0, "browsers": []}

        def fake_capture(**kwargs: object) -> dict:
            calls["browsers"].append(kwargs.get("browser"))
            copied = dict(fixture)
            copied["ok"] = True
            copied["browser"] = kwargs.get("browser") or "local"
            return copied

        def complete_factory(*_args: object, **_kwargs: object):
            def complete(messages, tools):
                names = [item["function"]["name"] for item in tools]
                self.assertIn("capture_page", names)
                self.assertIn("find_signer_chunk", names)
                self.assertIn("inspect_capture", names)
                self.assertIn("verify_signature", names)
                calls["n"] += 1
                if calls["n"] == 1:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c1",
                                "type": "function",
                                "function": {"name": "capture_page", "arguments": '{"browser":"local"}'},
                            }
                        ],
                    }
                if calls["n"] == 2:
                    captured = json.loads(messages[-1]["content"])
                    self.assertTrue(captured.get("ok"))
                    self.assertEqual(captured.get("hex_len"), len(fixture["hex"]))
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c2",
                                "type": "function",
                                "function": {"name": "recover_indices", "arguments": "{}"},
                            }
                        ],
                    }
                if calls["n"] == 3:
                    recovered = json.loads(messages[-1]["content"])
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c3",
                                "type": "function",
                                "function": {
                                    "name": "write_hot_js",
                                    "arguments": json.dumps({"source": recovered["hex_js"]}),
                                },
                            }
                        ],
                    }
                if calls["n"] == 4:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c4",
                                "type": "function",
                                "function": {"name": "verify_signature", "arguments": "{}"},
                            }
                        ],
                    }
                return {"role": "assistant", "content": "accepted"}

            return complete

        with TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            hot = tmp_path / "hot"
            data = tmp_path / "data"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            sabotaged = CANONICAL_JS.read_text(encoding="utf-8").replace("seed[5] % 4", "seed[0] % 4", 1)
            (hot / "hex.js").write_text(sabotaged, encoding="utf-8")
            store = Store(data)
            store.save_formula(Formula(path_index=0, seg_index=1, seek_indices=(0, 1, 2)))
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            os.environ["GROK2API_KEY"] = "test-key"
            with patch("statsig_signer.agent.grok2api_complete", complete_factory):
                result = update(
                    store=store,
                    runtime=runtime,
                    defer_capture=True,
                    capture_fn=fake_capture,
                    use_hermes=True,
                    force_hermes=True,
                )
            self.assertGreaterEqual(len(calls["browsers"]), 1)
            self.assertEqual(calls["browsers"][0], "local")
            self.assertTrue(result.get("ok"), result)
            self.assertTrue((result.get("accepted") or {}).get("ok"), result)
            self.assertIn("seed[5] % 4", (hot / "hex.js").read_text(encoding="utf-8"))

    def test_write_hot_js_rejects_one_seed_and_returns_stable_hex_js(self) -> None:
        paths = json.loads(PAIR_PATH.read_text(encoding="utf-8"))["paths"]
        truth = Formula(path_index=5, seg_index=33, seek_indices=(1, 14, 37))
        seed_a = bytes(range(48))
        seed_b = bytes((i * 3 + 7) % 256 for i in range(48))
        first = {
            "ok": True,
            "browser": "local",
            "url": "https://grok.com/imagine",
            "seed": encode_raw_std(seed_a),
            "hex": compute_hex(seed_a, paths, truth),
            "paths": paths,
            "script_urls": [],
            "chunks": [],
        }
        second = {
            "ok": True,
            "browser": "local",
            "url": "https://grok.com/",
            "seed": encode_raw_std(seed_b),
            "hex": compute_hex(seed_b, paths, truth),
            "paths": paths,
            "script_urls": [],
            "chunks": [],
        }
        one = recover_formula(seed_a, paths, first["hex"], Formula(path_index=0, seg_index=1, seek_indices=(0, 1, 2)))
        self.assertEqual(one.status, "recovered")
        self.assertNotEqual(compute_hex(seed_b, paths, one.formula), second["hex"])

        captures = {"n": 0}

        def fake_capture(**kwargs: object) -> dict:
            captures["n"] += 1
            url = str(kwargs.get("url") or "")
            copied = dict(second if "imagine" not in url and captures["n"] > 1 else first)
            if "imagine" not in url:
                copied = dict(second)
            copied["url"] = url or copied["url"]
            return copied

        calls = {"n": 0}

        def complete_factory(*_args: object, **_kwargs: object):
            def complete(messages, tools):
                calls["n"] += 1
                if calls["n"] == 1:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c1",
                                "type": "function",
                                "function": {"name": "capture_page", "arguments": '{"browser":"local"}'},
                            }
                        ],
                    }
                if calls["n"] == 2:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c2",
                                "type": "function",
                                "function": {"name": "recover_indices", "arguments": "{}"},
                            }
                        ],
                    }
                if calls["n"] == 3:
                    recovered = json.loads(messages[-1]["content"])
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c3",
                                "type": "function",
                                "function": {
                                    "name": "write_hot_js",
                                    "arguments": json.dumps({"source": recovered["hex_js"]}),
                                },
                            }
                        ],
                    }
                if calls["n"] == 4:
                    rejected = json.loads(messages[-1]["content"])
                    self.assertFalse(rejected.get("ok"), rejected)
                    self.assertNotIn("hex_js", rejected)
                    self.assertEqual(rejected.get("next"), "capture_page")
                    return {"role": "assistant", "content": "need more pages"}
                return {"role": "assistant", "content": "accepted"}

            return complete

        with TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            hot = tmp_path / "hot"
            data = tmp_path / "data"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            sabotaged = CANONICAL_JS.read_text(encoding="utf-8").replace("seed[5] % 4", "seed[0] % 4", 1)
            (hot / "hex.js").write_text(sabotaged, encoding="utf-8")
            store = Store(data)
            store.save_formula(Formula(path_index=0, seg_index=1, seek_indices=(0, 1, 2)))
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            os.environ["GROK2API_KEY"] = "test-key"
            with patch("statsig_signer.agent.grok2api_complete", complete_factory):
                result = update(
                    store=store,
                    runtime=runtime,
                    defer_capture=True,
                    capture_fn=fake_capture,
                    use_hermes=True,
                    force_hermes=True,
                )
            self.assertFalse(result.get("ok"), result)
            self.assertIn("seed[0] % 4", (hot / "hex.js").read_text(encoding="utf-8"))

    def test_verify_signature_fresh_page_must_match(self) -> None:
        paths = json.loads(PAIR_PATH.read_text(encoding="utf-8"))["paths"]
        stale = Formula(path_index=5, seg_index=33, seek_indices=(1, 9, 18))
        truth = Formula(path_index=5, seg_index=33, seek_indices=(1, 14, 37))
        seed_a = os.urandom(48)
        seed_b = os.urandom(48)
        for _ in range(32):
            if compute_hex(seed_b, paths, stale) != compute_hex(seed_b, paths, truth):
                break
            seed_b = os.urandom(48)
        else:
            self.fail("could not find a seed where stale HEX differs from truth")
        first = {
            "ok": True,
            "browser": "local",
            "url": "https://grok.com/imagine",
            "seed": encode_raw_std(seed_a),
            "hex": compute_hex(seed_a, paths, stale),
            "paths": paths,
            "script_urls": [],
            "chunks": [],
        }
        second = {
            "ok": True,
            "browser": "local",
            "url": "https://grok.com/",
            "seed": encode_raw_std(seed_b),
            "hex": compute_hex(seed_b, paths, truth),
            "paths": paths,
            "script_urls": [],
            "chunks": [],
        }
        captures = {"n": 0}

        def fake_capture(**kwargs: object) -> dict:
            captures["n"] += 1
            url = str(kwargs.get("url") or "")
            copied = dict(second if "imagine" not in url else first)
            copied["url"] = url or copied["url"]
            return copied

        calls = {"n": 0}

        def complete_factory(*_args: object, **_kwargs: object):
            def complete(messages, tools):
                names = [item["function"]["name"] for item in tools]
                self.assertIn("verify_signature", names)
                self.assertIn("capture_page", names)
                calls["n"] += 1
                if calls["n"] == 1:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c1",
                                "type": "function",
                                "function": {"name": "capture_page", "arguments": '{"browser":"local"}'},
                            }
                        ],
                    }
                if calls["n"] == 2:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c2",
                                "type": "function",
                                "function": {"name": "verify_signature", "arguments": "{}"},
                            }
                        ],
                    }
                verified = json.loads(messages[-1]["content"])
                self.assertFalse(verified.get("ok"), verified)
                self.assertFalse(verified.get("exit"), verified)
                self.assertIn("新鲜页", verified.get("error") or "")
                return {"role": "assistant", "content": "fresh page mismatch"}

            return complete

        with TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            hot = tmp_path / "hot"
            data = tmp_path / "data"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            (hot / "hex.js").write_text(_hex_js_for_formula(stale, runtime), encoding="utf-8")
            store = Store(data)
            store.save_formula(stale)
            store.save_pair(
                Pair(
                    seed=first["seed"],
                    hex=first["hex"],
                    paths=paths,
                    source="test",
                )
            )
            os.environ["GROK2API_KEY"] = "test-key"
            with patch("statsig_signer.agent.grok2api_complete", complete_factory):
                result = update(
                    store=store,
                    runtime=runtime,
                    defer_capture=True,
                    capture_fn=fake_capture,
                    use_hermes=True,
                    force_hermes=True,
                )
            self.assertGreaterEqual(captures["n"], 2)
            self.assertNotEqual(result.get("stopped"), "exit")
            self.assertEqual(result.get("content"), "fresh page mismatch")

    def test_verify_falls_back_when_other_url_capture_fails(self) -> None:
        fixture = fixture_from_pair_file(PAIR_PATH)
        calls = {"n": 0, "urls": []}

        def fake_capture(**kwargs: object) -> dict:
            url = str(kwargs.get("url") or "https://grok.com/imagine")
            calls["urls"].append(url)
            if "imagine" not in url:
                return {"ok": False, "browser": "local", "url": url}
            copied = dict(fixture)
            copied["ok"] = True
            copied["browser"] = "local"
            copied["url"] = url
            return copied

        def complete_factory(*_args: object, **_kwargs: object):
            def complete(messages, tools):
                calls["n"] += 1
                if calls["n"] == 1:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {"id": "c1", "type": "function", "function": {"name": "capture_page", "arguments": '{"browser":"local"}'}}
                        ],
                    }
                if calls["n"] == 2:
                    return {
                        "role": "assistant",
                        "tool_calls": [{"id": "c2", "type": "function", "function": {"name": "recover_indices", "arguments": "{}"}}],
                    }
                if calls["n"] == 3:
                    recovered = json.loads(messages[-1]["content"])
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {
                                "id": "c3",
                                "type": "function",
                                "function": {"name": "write_hot_js", "arguments": json.dumps({"source": recovered["hex_js"]})},
                            }
                        ],
                    }
                if calls["n"] == 4:
                    written = json.loads(messages[-1]["content"])
                    self.assertTrue(written.get("ok"), written)
                    return {
                        "role": "assistant",
                        "tool_calls": [{"id": "c4", "type": "function", "function": {"name": "verify_signature", "arguments": "{}"}}],
                    }
                return {"role": "assistant", "content": "accepted"}

            return complete

        with TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            hot = tmp_path / "hot"
            data = tmp_path / "data"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            (hot / "hex.js").write_text(CANONICAL_JS.read_text(encoding="utf-8"), encoding="utf-8")
            store = Store(data)
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            os.environ["GROK2API_KEY"] = "test-key"
            with patch("statsig_signer.agent.grok2api_complete", complete_factory):
                result = update(
                    store=store,
                    runtime=runtime,
                    defer_capture=True,
                    capture_fn=fake_capture,
                    use_hermes=True,
                    force_hermes=True,
                )
            self.assertTrue(result.get("ok"), result)
            self.assertTrue((result.get("accepted") or {}).get("ok"), result)
            self.assertTrue(any("imagine" not in u for u in calls["urls"]))

    def test_read_py_idle_is_cut_off(self) -> None:
        fixture = fixture_from_pair_file(PAIR_PATH)

        def fake_capture(**kwargs: object) -> dict:
            copied = dict(fixture)
            copied["ok"] = True
            copied["browser"] = "local"
            return copied

        calls = {"n": 0}

        def complete_factory(*_args: object, **_kwargs: object):
            def complete(messages, tools):
                calls["n"] += 1
                if calls["n"] == 1:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {"id": "c1", "type": "function", "function": {"name": "capture_page", "arguments": '{"browser":"local"}'}}
                        ],
                    }
                if calls["n"] == 2:
                    return {
                        "role": "assistant",
                        "tool_calls": [
                            {"id": "r1", "type": "function", "function": {"name": "read_py", "arguments": '{"path":"statsig_signer/htmlutil.py"}'}},
                            {"id": "r2", "type": "function", "function": {"name": "read_py", "arguments": '{"path":"statsig_signer/htmlutil.py"}'}},
                            {"id": "r3", "type": "function", "function": {"name": "read_py", "arguments": '{"path":"statsig_signer/htmlutil.py"}'}},
                        ],
                    }
                last = json.loads(messages[-1]["content"])
                self.assertIn(last.get("next"), ("capture_page", "verify_signature"))
                self.assertIn("空转", last.get("error") or "")
                return {"role": "assistant", "content": "stopped idle"}

            return complete

        with TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            hot = tmp_path / "hot"
            data = tmp_path / "data"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            (hot / "hex.js").write_text(CANONICAL_JS.read_text(encoding="utf-8"), encoding="utf-8")
            store = Store(data)
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            os.environ["GROK2API_KEY"] = "test-key"
            with patch("statsig_signer.agent.grok2api_complete", complete_factory):
                result = update(
                    store=store,
                    runtime=runtime,
                    defer_capture=True,
                    capture_fn=fake_capture,
                    use_hermes=True,
                    force_hermes=True,
                )
            self.assertEqual(result.get("content"), "stopped idle")
            self.assertNotEqual(result.get("stopped"), "exit")

    def test_agent_max_turns_is_200(self) -> None:
        os.environ["GROK2API_KEY"] = "test-key"
        os.environ.pop("STATSIG_AGENT_MAX_TURNS", None)
        with TemporaryDirectory() as tmp:
            hot = Path(tmp) / "hot"
            hot.mkdir()
            shutil.copy(WORKER_JS, hot / "eval_worker.js")
            (hot / "hex.js").write_text(CANONICAL_JS.read_text(encoding="utf-8"), encoding="utf-8")
            store = Store(Path(tmp) / "data")
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)

            def complete_factory(*_args: object, **_kwargs: object):
                return lambda messages, tools: {"role": "assistant", "content": "stop"}

            with patch("statsig_signer.agent.grok2api_complete", complete_factory):
                kernel = _kernel(store, {}, runtime, lambda **_k: {}, {})
            self.assertEqual(kernel.max_turns, 200)
