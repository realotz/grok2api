from __future__ import annotations

import json
import unittest
from pathlib import Path

from statsig_signer.algorithm import decode_seed
from statsig_signer.runtime import HotRuntime

ROOT = Path(__file__).resolve().parents[1]
PAIR = json.loads((ROOT / "data" / "pair.json").read_text(encoding="utf-8"))


class HotEvalTest(unittest.TestCase):
    def test_js_eval_matches_live_pair(self) -> None:
        runtime = HotRuntime(ROOT / "hot")
        self.addCleanup(runtime.close)
        seed = decode_seed(PAIR["seed"])
        hex_value = runtime.compute(seed, PAIR["paths"])
        self.assertEqual(hex_value, PAIR["hex"])

    def test_hex_js_rewrite_replaces_already_patched_indices(self) -> None:
        import shutil
        from tempfile import TemporaryDirectory

        from statsig_signer.agent import _hex_js_for_formula
        from statsig_signer.algorithm import Formula

        with TemporaryDirectory() as tmp:
            hot = Path(tmp) / "hot"
            hot.mkdir()
            shutil.copy(ROOT / "hot" / "hex.js", hot / "hex.js")
            shutil.copy(ROOT / "hot" / "eval_worker.js", hot / "eval_worker.js")
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            patched = Formula(path_index=5, seg_index=12, seek_indices=(1, 12, 14))
            first = _hex_js_for_formula(patched, runtime)
            runtime.write_js(first)
            self.assertIn("seed[12] % 16", runtime.current_js())
            stable = Formula(path_index=5, seg_index=33, seek_indices=(0, 14, 16))
            second = _hex_js_for_formula(stable, runtime)
            self.assertIn("seed[33] % 16", second)
            self.assertIn("seed[0] % 16", second)
            self.assertNotIn("seed[12] % 16", second)

    def test_hex_js_for_formula_keeps_helpers_if_hot_js_inlined(self) -> None:
        import shutil
        from tempfile import TemporaryDirectory

        from statsig_signer.agent import _hex_js_for_formula
        from statsig_signer.algorithm import Formula

        with TemporaryDirectory() as tmp:
            hot = Path(tmp) / "hot"
            hot.mkdir()
            shutil.copy(ROOT / "hot" / "eval_worker.js", hot / "eval_worker.js")
            (hot / "hex.js").write_text(
                "function computeHex(seed, paths) {\n"
                "  var path = paths[seed[5] % 4];\n"
                "  var want = seed[11] % 16;\n"
                "  return 'dead';\n"
                "}\n",
                encoding="utf-8",
            )
            runtime = HotRuntime(hot)
            self.addCleanup(runtime.close)
            source = _hex_js_for_formula(Formula(path_index=5, seg_index=33, seek_indices=(1, 14, 37)), runtime)
            self.assertIn("function pathSegments", source)
            self.assertIn("function hexFromSegment", source)
            self.assertIn("seed[33] % 16", source)
            self.assertNotIn("seed[11] % 16", source)

    def test_eval_timeout_recovers(self) -> None:
        import time

        runtime = HotRuntime(ROOT / "hot")
        self.addCleanup(runtime.close)
        source = "function computeHex(seed, paths) { while (true) {} }"
        start = time.time()
        with self.assertRaises(RuntimeError):
            runtime.eval_js(source, b"\x00" * 48, ["M 0 0"] * 4)
        self.assertLess(time.time() - start, 5)
        seed = decode_seed(PAIR["seed"])
        self.assertEqual(runtime.compute(seed, PAIR["paths"]), PAIR["hex"])

    def test_eval_rejects_wrong_source(self) -> None:
        runtime = HotRuntime(ROOT / "hot")
        self.addCleanup(runtime.close)
        seed = decode_seed(PAIR["seed"])
        source = runtime.current_js().replace("seed[5] % 4", "seed[0] % 4", 1)
        hex_value = runtime.eval_js(source, seed, PAIR["paths"])
        self.assertNotEqual(hex_value, PAIR["hex"])
