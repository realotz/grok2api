from __future__ import annotations

import json
import hashlib
import os
import unittest
from pathlib import Path

from statsig_signer.algorithm import Formula, aligned_seek, compute_hex, decode_seed
from statsig_signer.recover import recover_formula, recover_formula_stable

ROOT = Path(__file__).resolve().parents[1]
PAIR = json.loads((ROOT / "data" / "pair.json").read_text(encoding="utf-8"))


class RecoverTest(unittest.TestCase):
    def test_all_equivalent_path_candidates_are_retained(self) -> None:
        """HEX 等价曲线必须保留全部正确候选，不能只采用第一条路径。"""
        paths = [PAIR["paths"][0]] * 4
        truth = Formula(path_index=5, seg_index=33, seek_indices=(1, 14, 37))
        samples = []
        for index in range(8):
            seed = hashlib.sha512(str(index).encode()).digest()[:48]
            samples.append({
                "seed_bytes": seed,
                "paths": paths,
                "hex": compute_hex(seed, paths, truth),
                "seek": aligned_seek(seed, truth),
            })
        result = recover_formula_stable(samples, truth)
        self.assertEqual(result.status, "ambiguous", result)
        self.assertIsNone(result.formula)
        self.assertGreaterEqual(result.candidate_count, 48)

    def test_current_formula_matches_live_pair(self) -> None:
        seed = decode_seed(PAIR["seed"])
        result = recover_formula(seed, PAIR["paths"], PAIR["hex"], Formula(), seek_hint=240)
        self.assertEqual(result.status, "matched")
        self.assertEqual(result.formula, Formula())

    def test_recovers_indices_from_wrong_formula(self) -> None:
        seed = decode_seed(PAIR["seed"])
        wrong = Formula(path_index=0, seg_index=1, seek_indices=(0, 1, 2))
        result = recover_formula(seed, PAIR["paths"], PAIR["hex"], wrong, seek_hint=240)
        self.assertEqual(result.status, "recovered")
        self.assertIsNotNone(result.formula)
        self.assertEqual(result.hex, PAIR["hex"])
        self.assertEqual(result.formula.path_index, 5)
        self.assertEqual(result.formula.seg_index, 39)
        self.assertEqual(tuple(result.formula.seek_indices), (3, 31, 36))

    def test_stable_recover_rejects_one_sample_and_fits_two_seeds(self) -> None:
        paths = PAIR["paths"]
        truth = Formula(path_index=5, seg_index=33, seek_indices=(1, 14, 37))
        samples = []
        for raw in (
            bytes(range(48)),
            bytes((i * 3 + 7) % 256 for i in range(48)),
            bytes((i * 5 + 11) % 256 for i in range(48)),
        ):
            samples.append(
                {
                    "seed_bytes": raw,
                    "paths": paths,
                    "hex": compute_hex(raw, paths, truth),
                    "seek": None,
                }
            )
        one = recover_formula_stable(samples[:1], Formula())
        self.assertEqual(one.status, "needs_agent")
        two = recover_formula_stable(samples[:2], Formula())
        self.assertEqual(two.status, "ambiguous")
        self.assertIsNone(two.formula)
        result = None
        for _ in range(20):
            rand = []
            for _n in range(4):
                raw = os.urandom(48)
                rand.append(
                    {
                        "seed_bytes": raw,
                        "paths": paths,
                        "hex": compute_hex(raw, paths, truth),
                        "seek": None,
                    }
                )
            candidate = recover_formula_stable(rand, Formula())
            if candidate.status in ("recovered", "matched") and candidate.formula:
                result = candidate
                samples = rand
                break
        self.assertIsNotNone(result)
        self.assertEqual(result.formula.path_index, 5)
        self.assertEqual(result.formula.seg_index, 33)
        self.assertEqual(tuple(sorted(result.formula.seek_indices)), (1, 14, 37))
        for item in samples:
            self.assertEqual(compute_hex(item["seed_bytes"], paths, result.formula), item["hex"])
