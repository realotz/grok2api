from __future__ import annotations

import argparse
import json
import sys
import time

from pathlib import Path

from .capture import capture
from .runtime import sign_with_meta_hot
from .server import serve
from .store import Store


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="statsig_signer", description="grok.wodf.de 协议兼容的本地 x-statsig-id 签名器")
    sub = parser.add_subparsers(dest="command", required=True)

    serve_cmd = sub.add_parser("serve", help="监听 POST /sign")
    serve_cmd.add_argument("--listen", default="127.0.0.1:8788")

    sign_cmd = sub.add_parser("sign", help="一次性签名")
    sign_cmd.add_argument("--method", required=True)
    sign_cmd.add_argument("--path", required=True)
    sign_cmd.add_argument("--meta", required=True)

    cap = sub.add_parser("capture", help="用本机浏览器或 x2api 抓同一页 seed/HEX/curves")
    cap.add_argument("--browser", choices=("local", "x2api"), default="local")
    cap.add_argument("--url", default="https://grok.com/imagine")
    cap.add_argument("--headed", action="store_true")
    cap.add_argument("--x2api-addr", default="")

    upd = sub.add_parser("update", help="抓包后更新 pair；公式变了再走 Hermes 最小内核")
    upd.add_argument("--browser", choices=("local", "x2api"), default="local")
    upd.add_argument("--url", default="https://grok.com/imagine")
    upd.add_argument("--headed", action="store_true")
    upd.add_argument("--x2api-addr", default="")
    upd.add_argument("--no-hermes", action="store_true")
    upd.add_argument("--force-hermes", action="store_true", help="跳过穷举，强制走 grok-4.7 Hermes eval")
    upd.add_argument("--defer-capture", action="store_true", help="不预先抓包，让 agent 自己调用 capture_page")
    upd.add_argument("--fixture", default="", help="用已有 pair.json，不打开浏览器")

    watch_cmd = sub.add_parser("watch", help="监测 grok 前端 curves/chunk/sentry 是否发版")
    watch_cmd.add_argument("--interval", type=int, default=60)
    watch_cmd.add_argument("--once", action="store_true")
    watch_cmd.add_argument("--deep", action="store_true", help="这一轮用浏览器抓包对照官方 HEX")
    watch_cmd.add_argument("--repair", action="store_true", help="发现变化时自动抓包修复热代码")
    watch_cmd.add_argument("--browser", choices=("local", "x2api"), default="local")
    watch_cmd.add_argument("--deep-every", type=int, default=5, help="每 N 轮主动验签，默认每五轮；0 仅检查 HTML 指纹")

    args = parser.parse_args(argv)
    store = Store()
    if args.command == "serve":
        host, port = _listen(args.listen)
        server = serve(host, port, store)
        print(f"statsig signer listening on http://{host}:{port}/sign", file=sys.stderr)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            return 0
        return 0
    if args.command == "sign":
        value = sign_with_meta_hot(args.method, args.path, args.meta, int(time.time()), store.pair.paths, store.formula)
        print(json.dumps({"x-statsig-id": value}, ensure_ascii=False))
        return 0
    if args.command == "capture":
        captured = capture(
            browser=args.browser,
            url=args.url,
            headed=args.headed,
            **({"addr": args.x2api_addr} if args.browser == "x2api" and args.x2api_addr else {}),
        )
        public = {
            "ok": captured.get("ok"),
            "hex": captured.get("hex"),
            "hex_from_tostring": captured.get("hex_from_tostring"),
            "hex_agree": captured.get("hex_agree"),
            "seed": captured.get("seed"),
            "paths": captured.get("paths"),
            "curves_hash": captured.get("curves_hash"),
            "sentry_release": captured.get("sentry_release"),
            "seek": (captured.get("seeks") or [{}])[0].get("value") if captured.get("seeks") else None,
            "seek_via": (captured.get("seeks") or [{}])[0].get("via") if captured.get("seeks") else None,
            "salt": captured.get("salt"),
            "polls": captured.get("polls"),
            "elapsed_ms": captured.get("elapsed_ms"),
        }
        print(json.dumps(public, ensure_ascii=False, indent=2))
        return 0 if captured.get("ok") else 2
    if args.command == "update":
        from .agent import update

        fixture = None
        if args.fixture:
            from .agent import fixture_from_pair_file

            fixture = fixture_from_pair_file(Path(args.fixture))
        result = update(
            store=store,
            browser=args.browser,
            use_hermes=not args.no_hermes,
            force_hermes=args.force_hermes,
            defer_capture=args.defer_capture,
            fixture=fixture,
            url=args.url,
            headed=args.headed,
            **({"addr": args.x2api_addr} if args.browser == "x2api" and args.x2api_addr else {}),
        )
        print(json.dumps(result, ensure_ascii=False, indent=2, default=str))
        return 0 if result.get("ok") else 2
    if args.command == "watch":
        from .watch import tick, watch_loop

        if args.once:
            report = tick(store=store, deep=args.deep, repair=args.repair, browser=args.browser)
            print(json.dumps(report, ensure_ascii=False, indent=2, default=str))
            return 0 if report.get("ok") else 2
        watch_loop(interval=args.interval, repair=args.repair, deep_every=args.deep_every, browser=args.browser)
        return 0
    parser.error("unknown command")
    return 2


def _listen(value: str) -> tuple[str, int]:
    if ":" not in value:
        return "127.0.0.1", int(value)
    host, port = value.rsplit(":", 1)
    return host or "127.0.0.1", int(port)
