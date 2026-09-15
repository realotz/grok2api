from __future__ import annotations

import json
import unittest

from statsig_signer.watch import compare_fingerprints, fingerprint_from_html, should_backoff_repair, tick, watch_loop


class WatchTest(unittest.TestCase):
    def test_html_fingerprint_extracts_curves_and_release(self) -> None:
        seg = {"color": [1, 2, 3, 4, 5, 6], "deg": 10, "bezier": [1, 2, 3, 4]}
        curves = [[seg, seg, seg, seg] for _ in range(4)]
        html = (
            'sentry-release=grok-web@abc1234 '
            'src="https://cdn.grok.com/_next/static/chunks/aaa.js" '
            '"curves":' + json.dumps(curves)
        )
        fp = fingerprint_from_html(html)
        self.assertEqual(fp["sentry_release"], "grok-web@abc1234")
        self.assertTrue(fp["curves_hash"])
        self.assertTrue(fp["chunks_hash"])
        self.assertEqual(fp["path_count"], 4)
        self.assertEqual(fp["script_count"], 1)

    def test_compare_detects_curves_and_release_change(self) -> None:
        prev = {"sentry_release": "grok-web@aaa", "curves_hash": "h1", "chunks_hash": "c1", "source": "html"}
        same = compare_fingerprints(prev, dict(prev))
        self.assertEqual(same, [])
        changed = compare_fingerprints(
            prev,
            {"sentry_release": "grok-web@bbb", "curves_hash": "h2", "chunks_hash": "c2", "source": "html"},
        )
        self.assertIn("sentry_release", changed)
        self.assertIn("curves_hash", changed)
        self.assertIn("chunks", changed)
        self.assertEqual(compare_fingerprints(None, prev), ["first_seen"])

    def test_compare_ignores_html_vs_capture_url_lists(self) -> None:
        html_fp = {
            "sentry_release": "grok-web@abc",
            "curves_hash": "h1",
            "chunks_hash": "html-chunks",
            "source": "html",
        }
        capture_fp = {
            "sentry_release": "grok-web@abc",
            "curves_hash": "h1",
            "chunks_hash": "capture-chunks",
            "source": "capture",
            "signer_urls": ["https://cdn.grok.com/_next/static/chunks/signer.js"],
        }
        self.assertEqual(compare_fingerprints(html_fp, capture_fp), [])
        self.assertEqual(compare_fingerprints(capture_fp, html_fp), [])

    def test_repair_backs_off_after_failed_unchanged_tick(self) -> None:
        self.assertFalse(should_backoff_repair(None, True))
        self.assertFalse(should_backoff_repair({"repair_ok": False}, True))
        self.assertFalse(should_backoff_repair({"repair_ok": True}, False))
        self.assertTrue(should_backoff_repair({"repair_ok": False}, False))

    def test_tick_does_not_save_fingerprint_when_repair_fails(self) -> None:
        import json
        from pathlib import Path
        from tempfile import TemporaryDirectory
        from unittest.mock import patch

        from statsig_signer.store import Pair, Store

        with TemporaryDirectory() as tmp:
            data = Path(tmp)
            store = Store(data)
            store.save_pair(
                Pair(
                    seed="x" * 64,
                    hex="aa",
                    paths=["M 10,30 C 1,2 3,4 5,6 h 1 s 1,2 3,4"] * 4,
                    curves_hash="old-pair-curves",
                )
            )
            stale = {
                "sentry_release": "grok-web@old",
                "curves_hash": "old-pair-curves",
                "chunks_hash": "old-chunks",
                "source": "html",
            }
            (data / "frontend_fingerprint.json").write_text(json.dumps(stale), encoding="utf-8")
            live = {
                "ok": True,
                "fingerprint": {
                    "sentry_release": "grok-web@new",
                    "curves_hash": "new-html-curves",
                    "chunks_hash": "new-chunks",
                    "source": "html",
                },
                "hex_match": {"ok": None, "skipped": True},
                "observed_at": "now",
            }
            with patch("statsig_signer.watch.probe", return_value=live), patch(
                "statsig_signer.agent.update", return_value={"ok": False, "error": "failed"}
            ):
                report = tick(store=store, repair=True)
            self.assertTrue(report.get("changed"), report)
            self.assertIn("pair_curves", report.get("changes") or [])
            self.assertFalse((report.get("repair") or {}).get("ok"))
            saved = json.loads((data / "frontend_fingerprint.json").read_text(encoding="utf-8"))
            self.assertEqual(saved.get("curves_hash"), "old-pair-curves")

    def test_watch_loop_keeps_repair_flag_after_quiet_tick(self) -> None:
        from unittest.mock import patch

        calls = []

        def fake_tick(**kwargs: object) -> dict:
            calls.append(kwargs.get("repair"))
            if len(calls) >= 2:
                raise KeyboardInterrupt()
            return {"ok": True, "changed": False}

        with patch("statsig_signer.watch.tick", fake_tick), patch("statsig_signer.watch.time.sleep"):
            with self.assertRaises(KeyboardInterrupt):
                watch_loop(interval=30, repair=True, deep_every=0)
        self.assertEqual(calls, [True, True])
