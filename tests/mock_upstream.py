#!/usr/bin/env python3
"""Deterministic OpenAI-compatible mock upstream for MyRouter E2E tests.

Endpoints:
  GET  /v1/models              -> static catalog
  POST /v1/chat/completions    -> echo / 429 / 500 / slow, JSON or SSE

Auth: every request must send `Authorization: Bearer mock-key`.

Run: python tests/mock_upstream.py --port 20400 --log qa-artifacts/e2e/mock.jsonl
"""
from __future__ import annotations

import argparse
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG_LOCK = threading.Lock()
LOG_PATH = ""

MODELS = [
    {"id": "mock-echo", "object": "model", "owned_by": "mock"},
    {"id": "mock-flaky", "object": "model", "owned_by": "mock"},
    {"id": "mock-down", "object": "model", "owned_by": "mock"},
]


def log_event(payload: dict) -> None:
    if not LOG_PATH:
        return
    with LOG_LOCK:
        os.makedirs(os.path.dirname(LOG_PATH), exist_ok=True)
        with open(LOG_PATH, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(payload, ensure_ascii=False) + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # silence stderr access log
        return

    def _json(self, status: int, payload: dict, extra_headers: dict | None = None) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        for key, value in (extra_headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True

    def _authorized(self) -> bool:
        return self.headers.get("Authorization", "") == "Bearer mock-key"

    def do_GET(self) -> None:  # noqa: N802
        log_event({"ts": time.time(), "method": "GET", "path": self.path})
        if self.path.rstrip("/") == "/v1/models":
            if not self._authorized():
                self._json(401, {"error": {"message": "bad key", "type": "invalid_request_error"}})
                return
            self._json(200, {"object": "list", "data": MODELS})
            return
        self._json(404, {"error": {"message": "not found"}})

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            self._json(400, {"error": {"message": "bad json"}})
            return
        model = str(req.get("model", ""))
        stream = bool(req.get("stream"))
        log_event({
            "ts": time.time(), "method": "POST", "path": self.path,
            "model": model, "stream": stream, "auth": self.headers.get("Authorization", ""),
        })

        if self.path.rstrip("/") != "/v1/chat/completions":
            self._json(404, {"error": {"message": "not found"}})
            return
        if not self._authorized():
            self._json(401, {"error": {"message": "bad key", "type": "invalid_request_error"}})
            return
        if model == "mock-flaky":
            self._json(429, {"error": {"message": "mock rate limit", "type": "rate_limit_error"}},
                       {"Retry-After": "1"})
            return
        if model == "mock-down":
            self._json(500, {"error": {"message": "mock upstream failure", "type": "server_error"}})
            return
        if model == "mock-slow":
            time.sleep(3)
        self._chat(req, model, stream)

    def _chat(self, req: dict, model: str, stream: bool) -> None:
        messages = req.get("messages") or []
        text = ""
        for msg in reversed(messages):
            if isinstance(msg, dict) and msg.get("role") == "user":
                text = str(msg.get("content", ""))
                break
        content = f"echo: {text}"
        created = int(time.time())
        usage = {"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}

        if not stream:
            self._json(200, {
                "id": "chatcmpl-mock", "object": "chat.completion", "created": created,
                "model": model,
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": content},
                    "finish_reason": "stop",
                }],
                "usage": usage,
            })
            return

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

        def chunk(delta: dict, finish: str | None = None, include_usage: bool = False) -> None:
            payload = {
                "id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": created,
                "model": model,
                "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
            }
            if include_usage:
                payload["usage"] = usage
            self.wfile.write(f"data: {json.dumps(payload)}\n\n".encode())
            self.wfile.flush()

        chunk({"role": "assistant"})
        chunk({"content": content})
        chunk({}, "stop", include_usage=True)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


def main() -> None:
    global LOG_PATH
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=20400)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--log", default="qa-artifacts/e2e/mock.jsonl")
    args = parser.parse_args()
    LOG_PATH = args.log
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"mock upstream on http://{args.host}:{args.port}/v1 (log={args.log})", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
