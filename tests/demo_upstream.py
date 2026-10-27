#!/usr/bin/env python3
"""Multi-protocol demo upstream used to produce realistic screenshots.

Unlike ``mock_upstream.py`` (a single OpenAI-compatible endpoint for the E2E
harness), this server speaks three wire formats at once so the Providers,
Models, Routes and Activity pages show the mix a real deployment has:

  OpenAI      GET  /oai/v1/models                 POST /oai/v1/chat/completions
  Anthropic   GET  /ant/v1/models                 POST /ant/v1/messages
  Gemini      GET  /gem/v1beta/models             POST /gem/v1beta/models/<id>:generateContent

Model ids are real ones from the models.dev catalog (gpt-4o, claude-sonnet-4,
gemini-2.5-pro, ...), so MyRouter fills context windows and prices from the
official metadata instead of showing "Cost unavailable".

Failure injection (drives real fallback/error rows in Activity): put a marker in
the prompt text — ``#fail429``, ``#fail500`` or ``#slow``. MyRouter only
forwards a small allow-list of client headers upstream, so the trigger has to
travel in the body; a header ``X-Demo-Fail`` also works for direct calls.

Because the marker stays in the prompt for every attempt, an unscoped marker
would also fail the fallback target. Add ``:model-id`` to scope it, e.g.
``#fail429:gpt-4o-mini`` fails only that model, so the route really falls back
to the next provider and Activity records a genuine fallback row.

Run:
  python tests/demo_upstream.py --port 20401
"""
from __future__ import annotations

import argparse
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Keys are fixed so the seed script can create providers that really connect.
KEYS = {
    "oai": "demo-key-openai",
    "ant": "demo-key-anthropic",
    "gem": "demo-key-gemini",
}

MODELS = {
    "oai": [
        ("gpt-4o", "GPT-4o"),
        ("gpt-4o-mini", "GPT-4o mini"),
        ("gpt-4.1", "GPT-4.1"),
        ("o3", "o3"),
    ],
    "ant": [
        ("claude-sonnet-4-20250514", "Claude Sonnet 4"),
        ("claude-3-5-haiku-latest", "Claude 3.5 Haiku"),
    ],
    "gem": [
        ("gemini-2.5-pro", "Gemini 2.5 Pro"),
        ("gemini-2.5-flash", "Gemini 2.5 Flash"),
    ],
}

PRINT_LOCK = threading.Lock()


def log(line: str) -> None:
    with PRINT_LOCK:
        print(line, flush=True)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # keep stderr quiet
        return

    # -- helpers ---------------------------------------------------------
    def _send(self, status: int, payload: dict, extra: dict | None = None) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True

    def _family(self) -> str:
        path = self.path.split("?")[0]
        for fam in ("oai", "ant", "gem"):
            if path.startswith("/" + fam + "/"):
                return fam
        return ""

    def _authorized(self, fam: str) -> bool:
        if fam == "oai":
            return self.headers.get("Authorization", "") == "Bearer " + KEYS["oai"]
        if fam == "ant":
            return self.headers.get("x-api-key", "") == KEYS["ant"]
        if fam == "gem":
            return "x-goog-api-key=" + KEYS["gem"] in self.path
        return False

    def _fail_mode(self, prompt: str) -> str:
        """Failure trigger: the X-Demo-Fail header, or a marker in the prompt.

        A marker may be scoped to one model ("#fail429:gpt-4o-mini") so the
        fallback target still succeeds and the gateway records a real fallback.
        """
        mode = (self.headers.get("X-Demo-Fail") or "").strip().lower()
        if mode:
            return mode
        low = prompt.lower()
        for marker in ("#fail429", "#fail500", "#slow"):
            idx = low.find(marker)
            if idx < 0:
                continue
            tail = low[idx + len(marker):]
            if tail.startswith(":"):
                scope = tail[1:].split()[0] if len(tail) > 1 else ""
                if scope and scope != self._current_model:
                    continue
            return marker.lstrip("#")
        return ""

    # -- GET -------------------------------------------------------------
    def do_GET(self) -> None:  # noqa: N802
        fam = self._family()
        path = self.path.split("?")[0]
        if not fam:
            self._send(404, {"error": {"message": "not found"}})
            return
        if not self._authorized(fam):
            self._send(401, {"error": {"message": "invalid api key", "type": "invalid_request_error"}})
            return
        if path.endswith("/models"):
            if fam == "gem":
                self._send(200, {"models": [
                    {"name": "models/" + mid, "displayName": name, "inputTokenLimit": 1048576}
                    for mid, name in MODELS["gem"]
                ]})
                return
            if fam == "ant":
                self._send(200, {"data": [{"id": mid, "display_name": name} for mid, name in MODELS["ant"]]})
                return
            self._send(200, {"object": "list", "data": [
                {"id": mid, "object": "model", "owned_by": "demo"} for mid, _ in MODELS["oai"]
            ]})
            return
        self._send(404, {"error": {"message": "not found"}})

    # -- POST ------------------------------------------------------------
    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            self._send(400, {"error": {"message": "bad json"}})
            return
        fam = self._family()
        path = self.path.split("?")[0]
        if not fam:
            self._send(404, {"error": {"message": "not found"}})
            return
        if not self._authorized(fam):
            self._send(401, {"error": {"message": "invalid api key", "type": "invalid_request_error"}})
            return

        prompt = self._last_user_text(req)
        # The Gemini model id lives in the path, not the body, so the scoped
        # marker check needs it before deciding to fail.
        self._current_model = str(req.get("model", ""))
        if not self._current_model and ":generateContent" in path:
            self._current_model = path.split("/models/")[-1].split(":")[0]
        mode = self._fail_mode(prompt)
        if mode == "fail429":
            self._send(429, {"error": {"message": "demo rate limit", "type": "rate_limit_error"}},
                       {"Retry-After": "1"})
            return
        if mode == "fail500":
            self._send(500, {"error": {"message": "demo upstream failure", "type": "server_error"}})
            return
        if mode == "slow":
            time.sleep(2)

        log(f"{fam} {path} model={req.get('model', '')} prompt={prompt[:60]!r}")

        if fam == "oai" and path.endswith("/chat/completions"):
            self._openai(req, prompt)
            return
        if fam == "ant" and path.endswith("/messages"):
            self._anthropic(req, prompt)
            return
        if fam == "gem" and ":generateContent" in path:
            model = path.split("/models/")[-1].split(":")[0]
            self._gemini(model, prompt)
            return
        self._send(404, {"error": {"message": "not found"}})

    @staticmethod
    def _last_user_text(req: dict) -> str:
        for msg in reversed(req.get("messages") or req.get("contents") or []):
            if not isinstance(msg, dict):
                continue
            if msg.get("role") in ("user", "model"):
                parts = msg.get("parts")
                if parts:
                    return " ".join(str(p.get("text", "")) for p in parts if isinstance(p, dict))
                return str(msg.get("content", ""))
        return ""

    def _answer(self, prompt: str) -> str:
        text = prompt.strip() or "your request"
        return (
            f"Here is a concise answer for: {text}\n\n"
            "1. Parse the request and confirm the intent.\n"
            "2. Route it to the cheapest model that can satisfy the quality bar.\n"
            "3. Return the result with token usage and latency attached."
        )

    def _openai(self, req: dict, prompt: str) -> None:
        model = str(req.get("model", "gpt-4o"))
        answer = self._answer(prompt)
        created = int(time.time())
        usage = {"prompt_tokens": 812, "completion_tokens": 168, "total_tokens": 980}
        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            for chunk in ({"role": "assistant"}, {"content": answer}, {}):
                payload = {
                    "id": "chatcmpl-demo", "object": "chat.completion.chunk", "created": created,
                    "model": model,
                    "choices": [{"index": 0, "delta": chunk,
                                 "finish_reason": "stop" if not chunk else None}],
                }
                if not chunk:
                    payload["usage"] = usage
                self.wfile.write(f"data: {json.dumps(payload)}\n\n".encode())
                self.wfile.flush()
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            return
        self._send(200, {
            "id": "chatcmpl-demo", "object": "chat.completion", "created": created, "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": answer},
                         "finish_reason": "stop"}],
            "usage": usage,
        })

    def _anthropic(self, req: dict, prompt: str) -> None:
        model = str(req.get("model", "claude-sonnet-4-20250514"))
        usage = {"input_tokens": 812, "output_tokens": 168}
        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            frames = [
                {"type": "message_start", "message": {"usage": {"input_tokens": 812, "output_tokens": 0}}},
                {"type": "content_block_delta", "delta": {"type": "text_delta", "text": self._answer(prompt)}},
                {"type": "message_delta", "delta": {"stop_reason": "end_turn"},
                 "usage": {"output_tokens": 168}},
            ]
            for frame in frames:
                self.wfile.write(f"data: {json.dumps(frame)}\n\n".encode())
                self.wfile.flush()
            return
        self._send(200, {
            "id": "msg_demo", "model": model, "stop_reason": "end_turn",
            "content": [{"type": "text", "text": self._answer(prompt)}],
            "usage": usage,
        })

    def _gemini(self, model: str, prompt: str) -> None:
        self._send(200, {
            "candidates": [{"content": {"parts": [{"text": self._answer(prompt)}]}, "finishReason": "STOP"}],
            "usageMetadata": {"promptTokenCount": 812, "candidatesTokenCount": 168, "totalTokenCount": 980},
            "modelVersion": model,
        })


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=20401)
    parser.add_argument("--host", default="127.0.0.1")
    args = parser.parse_args()
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    log(f"demo upstream on http://{args.host}:{args.port} "
        f"(openai /oai/v1, anthropic /ant/v1, gemini /gem/v1beta)")
    server.serve_forever()


if __name__ == "__main__":
    main()
