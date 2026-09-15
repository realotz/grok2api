from __future__ import annotations

import unittest

from statsig_signer.agent import read_signer_file, write_signer_file
from statsig_signer.capture import HOOK_JS, PROBE_JS, _init_script, verify_record


class CaptureHookTest(unittest.TestCase):
    def test_init_script_is_a_statement_not_an_arrow_function(self) -> None:
        body = _init_script()
        self.assertFalse(body.lstrip().startswith("() =>"))
        self.assertIn("crypto.subtle.digest", body)
        self.assertIn("obfiowerehiring", body)
        self.assertIn("Number.prototype.toString", body)
        self.assertIn("hexFromToString", body)
        self.assertIn("Element.prototype.animate", body)

    def test_probe_harvests_getanimations_as_seek_fallback(self) -> None:
        self.assertIn("document.getAnimations", PROBE_JS)
        self.assertIn("/rest/modes", PROBE_JS)

    def test_write_py_rejects_hex_js_and_escape(self) -> None:
        self.assertFalse(write_signer_file("hot/hex.js", "function computeHex() {}")["ok"])
        self.assertFalse(write_signer_file("../etc/passwd", "x")["ok"])
        self.assertFalse(read_signer_file("hot/hex.js")["ok"])

    def test_write_py_syntax_error_does_not_clobber(self) -> None:
        before = read_signer_file("statsig_signer/htmlutil.py")
        self.assertTrue(before["ok"])
        out = write_signer_file("statsig_signer/htmlutil.py", "def (\n")
        self.assertFalse(out["ok"])
        self.assertIn("语法错误", out.get("error") or "")
        after = read_signer_file("statsig_signer/htmlutil.py")
        self.assertEqual(after.get("source"), before.get("source"))

    def test_read_py_htmlutil(self) -> None:
        out = read_signer_file("statsig_signer/htmlutil.py")
        self.assertTrue(out["ok"], out)
        self.assertIn("chunks_hash", out.get("source") or "")

    def test_verify_record_omits_seed(self) -> None:
        record = verify_record(
            {
                "ok": True,
                "url": "https://grok.com/imagine",
                "seed": "secret-seed-value",
                "hex": "abc",
                "hex_from_tostring": "abc",
                "hex_agree": True,
                "seeks": [{"value": 240, "via": "getAnimations"}],
                "anims": [{"via": "animate"}],
            }
        )
        encoded = str(record)
        self.assertNotIn("secret-seed-value", encoded)
        self.assertEqual(record["seed_len"], 17)
        self.assertTrue(record["hex_agree"])
        self.assertEqual(record["seeks"][0]["via"], "getAnimations")
