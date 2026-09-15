from __future__ import annotations

import json
import re
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from typing import Any, Callable, Iterable
from uuid import uuid4

ToolFn = Callable[..., Any]


@dataclass
class Tool:
    name: str
    description: str
    parameters: dict[str, Any]
    fn: ToolFn


@dataclass
class HermesResult:
    content: str
    messages: list[dict[str, Any]] = field(default_factory=list)
    turns: int = 0
    stopped: str = ""


class HermesKernel:
    """OpenAI tool-calling loop: complete → tools → observe, until the model stops."""

    def __init__(
        self,
        complete: Callable[[list[dict[str, Any]], list[dict[str, Any]]], dict[str, Any]],
        tools: Iterable[Tool],
        max_turns: int = 8,
    ) -> None:
        self.complete = complete
        self.tools = {tool.name: tool for tool in tools}
        self.max_turns = max_turns

    def schemas(self) -> list[dict[str, Any]]:
        return [
            {
                "type": "function",
                "function": {
                    "name": tool.name,
                    "description": tool.description,
                    "parameters": tool.parameters,
                },
            }
            for tool in self.tools.values()
        ]

    def run(self, system: str, user: str) -> HermesResult:
        messages: list[dict[str, Any]] = [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ]
        for turn in range(1, self.max_turns + 1):
            print(f"hermes turn {turn}/{self.max_turns}", file=sys.stderr, flush=True)
            assistant = self.complete(messages, self.schemas())
            assistant = _normalize_assistant(assistant)
            messages.append(assistant)
            calls = assistant.get("tool_calls") or []
            if not calls:
                return HermesResult(content=str(assistant.get("content") or ""), messages=messages, turns=turn, stopped="stop")
            stop_reason = ""
            stop_content = ""
            for call in calls:
                name = ((call.get("function") or {}).get("name")) or ""
                raw_args = (call.get("function") or {}).get("arguments") or "{}"
                tool_id = call.get("id") or f"call_{uuid4().hex[:8]}"
                try:
                    args = json.loads(raw_args) if isinstance(raw_args, str) else dict(raw_args)
                    if name not in self.tools:
                        payload: Any = {"error": f"unknown tool {name}"}
                    else:
                        print(f"hermes tool {name}", file=sys.stderr, flush=True)
                        payload = self.tools[name].fn(**args)
                        print(f"hermes tool {name} {_summarize_payload(payload)}", file=sys.stderr, flush=True)
                except Exception as exc:
                    payload = {"error": str(exc)}
                    print(f"hermes tool {name} error {exc}", file=sys.stderr, flush=True)
                messages.append(
                    {
                        "role": "tool",
                        "tool_call_id": tool_id,
                        "content": json.dumps(payload, ensure_ascii=False, default=str),
                    }
                )
                if _tool_requests_exit(name, payload):
                    stop_reason = "exit"
                    if isinstance(payload, dict):
                        stop_content = str(payload.get("reason") or payload.get("error") or "")
                    break
            if stop_reason:
                print(f"hermes exit after turn {turn}", file=sys.stderr, flush=True)
                return HermesResult(
                    content=stop_content or str(assistant.get("content") or "exit"),
                    messages=messages,
                    turns=turn,
                    stopped=stop_reason,
                )
        return HermesResult(content="max_turns", messages=messages, turns=self.max_turns, stopped="max_turns")


def _summarize_payload(payload: Any) -> str:
    if not isinstance(payload, dict):
        text = str(payload)
        return text if len(text) <= 240 else text[:240] + "…"
    summary: dict[str, Any] = {}
    for key in ("ok", "error", "next", "status", "match", "reason", "exit"):
        if key in payload:
            value = payload[key]
            summary[key] = value if not isinstance(value, str) or len(value) <= 200 else value[:200] + "…"
    if "hex_js" in payload:
        summary["hex_js_len"] = len(str(payload["hex_js"]))
    if payload.get("second") and isinstance(payload["second"], dict):
        summary["second_url"] = payload["second"].get("url")
        summary["second_match"] = payload["second"].get("hex") == payload["second"].get("official")
    return json.dumps(summary, ensure_ascii=False)


def _tool_requests_exit(name: str, payload: Any) -> bool:
    if name in ("exit", "finish"):
        return True
    return isinstance(payload, dict) and payload.get("exit") is True


def grok2api_complete(
    base_url: str,
    api_key: str,
    model: str,
    timeout: int = 120,
) -> Callable[[list[dict[str, Any]], list[dict[str, Any]]], dict[str, Any]]:
    endpoint = base_url.rstrip("/") + "/chat/completions"

    def complete(messages: list[dict[str, Any]], tools: list[dict[str, Any]]) -> dict[str, Any]:
        body = {
            "model": model,
            "messages": messages,
            "tools": tools,
            "tool_choice": "auto",
            "temperature": 0,
            "stream": False,
        }
        request = urllib.request.Request(
            endpoint,
            data=json.dumps(body).encode("utf-8"),
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {api_key}",
            },
            method="POST",
        )
        last_error: Exception | None = None
        for attempt in range(2):
            try:
                with urllib.request.urlopen(request, timeout=timeout) as response:
                    payload = json.loads(response.read().decode("utf-8"))
                break
            except urllib.error.HTTPError as exc:
                detail = exc.read().decode("utf-8", errors="replace")[:800]
                if exc.code in (429, 500, 502, 503, 504) and attempt == 0:
                    last_error = RuntimeError(f"grok2api {exc.code}: {detail}")
                    print(f"hermes complete HTTP {exc.code} retry {attempt + 1}", file=sys.stderr, flush=True)
                    continue
                raise RuntimeError(f"grok2api {exc.code}: {detail}") from exc
            except TimeoutError as exc:
                last_error = exc
                print(f"hermes complete timeout retry {attempt + 1}", file=sys.stderr, flush=True)
                continue
        else:
            raise last_error or TimeoutError("grok2api complete timeout")
        choices = payload.get("choices") or []
        if not choices:
            raise RuntimeError("grok2api 没有返回 choices")
        message = choices[0].get("message") or {}
        if "role" not in message:
            message["role"] = "assistant"
        return message

    return complete


_TOOL_CALL_BLOCK = re.compile(r"(?is)<tool_calls\s*>(.*?)</tool_calls\s*>")
_TOOL_CALL = re.compile(r"(?is)<tool_call\s*>(.*?)</tool_call\s*>")
_TOOL_NAME = re.compile(r"(?is)<tool_name\s*>(.*?)</tool_name\s*>")
_TOOL_PARAMS = re.compile(r"(?is)<parameters\s*>(.*?)</parameters\s*>")


def _normalize_assistant(message: dict[str, Any]) -> dict[str, Any]:
    if message.get("tool_calls"):
        return message
    content = str(message.get("content") or "")
    block = _TOOL_CALL_BLOCK.search(content)
    if not block:
        return message
    calls = []
    for index, chunk in enumerate(_TOOL_CALL.findall(block.group(1)), start=1):
        name_match = _TOOL_NAME.search(chunk)
        params_match = _TOOL_PARAMS.search(chunk)
        if not name_match:
            continue
        calls.append(
            {
                "id": f"call_{index}",
                "type": "function",
                "function": {
                    "name": name_match.group(1).strip(),
                    "arguments": (params_match.group(1).strip() if params_match else "{}"),
                },
            }
        )
    if calls:
        message = dict(message)
        message["tool_calls"] = calls
        message["content"] = None
    return message
