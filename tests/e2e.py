#!/usr/bin/env python3
"""MyRouter end-to-end acceptance harness (stdlib only).

Exercises the real binary over HTTP: first-run setup, login/CSRF, provider +
model discovery, routes, API keys, chat (JSON + SSE), fallback, Guard limits,
IP rules, activity/usage, and core security headers.

Usage:
  python tests/e2e.py --base-url http://127.0.0.1:20299 \
      --username admin --password '<generated>' \
      --mock-url http://127.0.0.1:20400/v1 \
      --out qa-artifacts/e2e/report.json

Exit code 0 only when every check passes.
"""
from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.request

RESULTS: list[dict] = []
CSRF = ""


def check(name: str, ok: bool, detail: str = "") -> bool:
    RESULTS.append({"check": name, "ok": bool(ok), "detail": detail})
    line = ("PASS  " if ok else "FAIL  ") + name
    if not ok and detail:
        clean = "".join(str(detail).split())
        line += " | " + clean[:220]
    print(line, flush=True)
    return bool(ok)


def request(opener, method: str, url: str, body=None, headers=None, timeout=40, include_csrf=False):
    data = None
    hdrs = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        hdrs["Content-Type"] = "application/json"
    if headers:
        hdrs.update(headers)
    if include_csrf and CSRF and method not in ("GET", "HEAD"):
        hdrs["X-CSRF-Token"] = CSRF
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    # Windows loopback occasionally resets a connection before the response is
    # read. A status of 0 means no HTTP response was received; retry once so a
    # transport flake does not masquerade as a product failure.
    for attempt in range(2):
        try:
            with opener.open(req, timeout=timeout) as res:
                raw = res.read()
                return res.status, dict(res.headers), raw
        except urllib.error.HTTPError as exc:
            return exc.code, dict(exc.headers), exc.read()
        except Exception as exc:  # connection refused, timeout, reset
            text = str(exc)
            flaky = "10054" in text or "forcibly closed" in text.lower() or "reset" in text.lower()
            if attempt == 0 and flaky:
                time.sleep(0.15)
                continue
            return 0, {}, text.encode()


def as_json(raw: bytes):
    try:
        return json.loads(raw.decode() or "null")
    except Exception:
        return None


def new_opener():
    jar = http.cookiejar.CookieJar()
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar)), jar


def main() -> int:
    global CSRF
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:20299")
    ap.add_argument("--username", default="admin")
    ap.add_argument("--password", default="", help="Admin password (generated when omitted)")
    ap.add_argument("--mock-url", default="http://127.0.0.1:20400/v1")
    ap.add_argument("--out", default="qa-artifacts/e2e/report.json")
    args = ap.parse_args()
    # Never ship a literal default credential: generate a throwaway password
    # for this run when the caller does not supply one.
    if not args.password:
        args.password = "e2e-" + secrets.token_urlsafe(18)
    base = args.base_url.rstrip("/")
    started = time.time()

    def url(path: str) -> str:
        return base + path

    anon, _ = new_opener()
    sess, _ = new_opener()

    # --- public surface -----------------------------------------------------
    st, hdrs, raw = request(anon, "GET", url("/healthz"))
    body = as_json(raw) or {}
    check("healthz 200 + status ok", st == 200 and body.get("status") == "ok", f"{st} {raw[:200]}")
    check("healthz has security headers",
          hdrs.get("X-Content-Type-Options") == "nosniff" and "frame-ancestors" in hdrs.get("Content-Security-Policy", ""),
          str(hdrs))

    # SPA caching: index.html must revalidate after an upgrade, while hashed
    # assets may be immutable.
    st, hdrs, raw = request(anon, "GET", url("/"))
    check("index.html is not cached immutably", st == 200 and "immutable" not in hdrs.get("Cache-Control", ""),
          f"{st} cache={hdrs.get('Cache-Control')}")
    m = __import__("re").search(r'src="(/assets/[^"]+\.js)"', raw.decode(errors="replace"))
    if m:
        st, ah, _ = request(anon, "GET", url(m.group(1)))
        check("hashed asset is immutable", st == 200 and "immutable" in ah.get("Cache-Control", ""),
              f"{st} cache={ah.get('Cache-Control')}")
    else:
        check("hashed asset is immutable", False, "no script asset found in index.html")

    st, _, raw = request(anon, "GET", url("/admin/setup/status"))
    body = as_json(raw) or {}
    check("setup status reachable", st == 200 and "needs_setup" in body, f"{st} {raw[:200]}")
    was_fresh = bool(body.get("needs_setup"))

    if was_fresh:
        st, _, raw = request(anon, "POST", url("/admin/setup/complete"),
                             {"username": args.username, "password": args.password, "port": 20299})
        check("first-run setup completes from loopback", st in (200, 201), f"{st} {raw[:300]}")
    else:
        check("instance already set up (pre-existing admin)", True, "skipping setup")

    st, _, raw = request(anon, "GET", url("/admin/setup/status"))
    body = as_json(raw) or {}
    check("setup locked after completion", st == 200 and body.get("needs_setup") is False, f"{st} {raw[:200]}")

    # --- auth + CSRF --------------------------------------------------------
    st, _, raw = request(sess, "POST", url("/admin/auth/login"),
                         {"username": args.username, "password": args.password})
    body = as_json(raw) or {}
    CSRF = body.get("csrf_token", "")
    check("login sets CSRF token", st == 200 and bool(CSRF), f"{st} {raw[:200]}")
    st, _, raw = request(sess, "GET", url("/admin/auth/me"))
    check("session works", st == 200 and (as_json(raw) or {}).get("username") == args.username, f"{st} {raw[:200]}")

    st, _, _ = request(anon, "GET", url("/admin/providers"))
    check("admin API rejects anonymous", st == 401, str(st))
    st, _, raw = request(sess, "POST", url("/admin/providers"), {"name": "csrf-probe", "type": "openai"})
    check("admin POST rejects missing CSRF", st == 403, f"{st} {raw[:200]}")

    # --- settings: allow private upstreams ---------------------------------
    st, _, raw = request(sess, "PATCH", url("/admin/settings"),
                         {"allow_private_upstreams": "true"}, include_csrf=True)
    check("allow_private_upstreams enabled", st == 200, f"{st} {raw[:200]}")

    st, _, raw = request(sess, "PATCH", url("/admin/settings"), {"base_url": "not-a-url"}, include_csrf=True)
    check("invalid base_url rejected", st == 400, f"{st} {raw[:200]}")

    # --- draft connection test (must not persist) --------------------------
    st, _, raw = request(sess, "POST", url("/admin/providers/test"), {
        "type": "openai-compatible", "base_url": args.mock_url, "api_key": "mock-key",
    }, include_csrf=True)
    draft = as_json(raw) or {}
    check("draft test succeeds without saving", st == 200 and draft.get("status") == "connected", f"{st} {raw[:300]}")
    st, _, raw = request(sess, "GET", url("/admin/providers"))
    check("draft test persists nothing", st == 200 and len((as_json(raw) or {}).get("data") or []) == 0, f"{st}")

    st, _, raw = request(sess, "POST", url("/admin/providers/test"), {
        "type": "openai-compatible", "base_url": args.mock_url, "api_key": "wrong-key",
    }, include_csrf=True)
    draft = as_json(raw) or {}
    check("draft test rejects a bad key", st == 200 and draft.get("status") == "invalid_credentials", f"{st} {raw[:300]}")

    st, _, raw = request(sess, "POST", url("/admin/providers/test"), {
        "type": "openai-compatible", "base_url": args.mock_url,
    }, include_csrf=True)
    check("draft test requires an API key", st == 400, f"{st} {raw[:200]}")

    # A fresh database must return [] (not null): the SPA calls .map/.filter on
    # this field, and null used to blank Routes/Activity/API Keys.
    st, _, raw = request(sess, "GET", url("/admin/routes"))
    routes_body = as_json(raw) or {}
    check("empty route list is [] not null", st == 200 and routes_body.get("data") == [], f"{st} {raw[:200]}")

    # --- provider + models --------------------------------------------------
    st, _, raw = request(sess, "POST", url("/admin/providers"), {
        "name": "Mock Lab", "type": "openai-compatible", "base_url": args.mock_url, "api_key": "mock-key",
    }, include_csrf=True)
    prov = (as_json(raw) or {}).get("data") or {}
    prov_id = prov.get("id", "")
    check("create provider", st == 201 and bool(prov_id), f"{st} {raw[:300]}")

    st, _, raw = request(sess, "POST", url(f"/admin/providers/{prov_id}/test"), {}, include_csrf=True)
    body = as_json(raw) or {}
    check("provider test reaches mock upstream", st == 200 and body.get("status") == "connected", f"{st} {raw[:300]}")

    st, _, raw = request(sess, "POST", url(f"/admin/providers/{prov_id}/refresh-models"), {}, include_csrf=True)
    body = as_json(raw) or {}
    models = {m.get("model_id"): m.get("id") for m in body.get("data") or []}
    check("refresh discovers models", st == 200 and {"mock-echo", "mock-flaky", "mock-down"} <= set(models),
          f"{st} count={body.get('count')} {raw[:300]}")

    st, _, raw = request(sess, "GET", url("/admin/models"))
    check("models list non-empty", st == 200 and len((as_json(raw) or {}).get("data") or []) >= 3, f"{st}")

    # --- routes -------------------------------------------------------------
    st, _, raw = request(sess, "POST", url("/admin/routes"), {
        "alias": "coding", "description": "E2E primary route",
        "primary_model_id": models.get("mock-echo"),
        "fallback_model_ids": [], "retry": 0, "timeout_ms": 15000,
    }, include_csrf=True)
    check("create route coding", st == 201, f"{st} {raw[:300]}")

    st, _, raw = request(sess, "POST", url("/admin/routes"), {
        "alias": "fallback-test", "description": "429 -> fallback",
        "primary_model_id": models.get("mock-flaky"),
        "fallback_model_ids": [models.get("mock-echo")],
        "fallback_on": ["rate_limit", "timeout", "upstream_5xx", "network"],
        "retry": 0, "timeout_ms": 15000,
    }, include_csrf=True)
    check("create route fallback-test", st == 201, f"{st} {raw[:300]}")

    # --- API keys -----------------------------------------------------------
    st, _, raw = request(sess, "POST", url("/admin/api-keys"), {
        "name": "E2E main", "allowed_routes": ["coding", "fallback-test"],
    }, include_csrf=True)
    body = as_json(raw) or {}
    secret = body.get("secret", "")
    key_id = (body.get("data") or {}).get("id", "")
    check("create API key returns secret once", st == 201 and secret.startswith("mr-sk-"), f"{st}")

    st, _, raw = request(sess, "POST", url("/admin/policies"), {
        "name": "E2E strict", "rpm": 1, "burst": 1, "tpm": 0,
        "max_ips": 0, "max_devices": 0, "on_exceed": "reject",
    }, include_csrf=True)
    policy_id = ((as_json(raw) or {}).get("data") or {}).get("id", "")
    check("create RPM policy", st == 201 and bool(policy_id), f"{st} {raw[:300]}")

    st, _, raw = request(sess, "POST", url("/admin/api-keys"), {
        "name": "E2E guard", "allowed_routes": ["coding"], "policy_id": policy_id,
    }, include_csrf=True)
    guard_secret = (as_json(raw) or {}).get("secret", "")
    check("create guarded API key", st == 201 and bool(guard_secret), f"{st}")

    # --- gateway ------------------------------------------------------------
    gw_headers = {"Authorization": f"Bearer {secret}", "User-Agent": "myrouter-e2e/1.0"}
    st, _, raw = request(anon, "GET", url("/v1/models"), headers=gw_headers)
    listed = {m.get("id") for m in (as_json(raw) or {}).get("data") or []}
    check("/v1/models lists route aliases", st == 200 and {"coding", "fallback-test"} <= listed, f"{st} {raw[:300]}")

    st, hdrs, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "hello e2e"}],
    }, headers=gw_headers)
    body = as_json(raw) or {}
    content = (((body.get("choices") or [{}])[0].get("message") or {}).get("content") or "")
    check("chat non-stream succeeds", st == 200 and "echo: hello e2e" in content, f"{st} {raw[:300]}")
    check("chat returns X-Request-Id", bool(hdrs.get("X-Request-Id")), str(hdrs))
    first_request_id = hdrs.get("X-Request-Id", "")

    st, _, raw = request(sess, "GET", url("/admin/live"))
    live = as_json(raw) or {}
    recent = live.get("recent") or []
    check("live telemetry records the request",
          st == 200 and any(r.get("request_id") == first_request_id for r in recent) and len(live.get("providers") or []) >= 1,
          f"{st} recent={len(recent)} {raw[:300]}")

    # Branding assets must be served so the browser tab and PWA show the icon.
    st, ih, ico = request(anon, "GET", url("/favicon.ico"))
    check("favicon.ico served", st == 200 and ico[:4] == b"\x00\x00\x01\x00", f"{st} {ico[:8]}")
    st, _, svg = request(anon, "GET", url("/favicon.svg"))
    check("favicon.svg served", st == 200 and b"<svg" in svg[:200], f"{st}")
    st, _, manifest = request(anon, "GET", url("/site.webmanifest"))
    check("web manifest served", st == 200 and b"MyRouter" in manifest, f"{st}")

    # --- Test Lab (admin playground) ---------------------------------------
    st, _, raw = request(sess, "POST", url("/admin/playground/chat"), {
        "route": "coding", "messages": [{"role": "user", "content": "playground ping"}],
        "max_tokens": 64, "temperature": 0.2,
    }, include_csrf=True)
    pg = (as_json(raw) or {}).get("data") or {}
    check("playground non-stream returns a result",
          st == 200 and pg.get("status") == "ok" and "echo: playground ping" in (pg.get("content") or "")
          and bool(pg.get("provider")) and len(pg.get("trace") or []) >= 1,
          f"{st} {raw[:400]}")
    check("playground reports latency and tokens",
          pg.get("latency_ms", -1) >= 0 and (pg.get("in_tokens", 0) + pg.get("out_tokens", 0)) > 0,
          str(pg)[:300])

    st, sh, raw = request(sess, "POST", url("/admin/playground/stream"), {
        "route": "coding", "messages": [{"role": "user", "content": "playground stream"}],
        "max_tokens": 64,
    }, include_csrf=True, timeout=45)
    stream_text = raw.decode(errors="replace")
    check("playground stream emits delta and done frames",
          st == 200 and '"type":"delta"' in stream_text and '"type":"done"' in stream_text
          and "[DONE]" in stream_text and "echo: playground stream" in stream_text,
          f"{st} {stream_text[:400]}")
    check("playground stream reports TTFT", bool(__import__("re").search(r'"ttft_ms":([1-9])', stream_text)),
          "no positive ttft_ms in stream frames")

    st, hdrs, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "stream": True,
        "messages": [{"role": "user", "content": "stream e2e"}],
    }, headers=gw_headers, timeout=60)
    text = raw.decode(errors="replace")
    check("chat stream returns SSE", st == 200 and "data:" in text and "[DONE]" in text and "echo: stream e2e" in text,
          f"{st} {text[:300]}")
    check("stream content-type is event-stream", "text/event-stream" in hdrs.get("Content-Type", ""), str(hdrs))

    st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "fallback-test", "messages": [{"role": "user", "content": "fallback e2e"}],
    }, headers=gw_headers)
    body = as_json(raw) or {}
    content = (((body.get("choices") or [{}])[0].get("message") or {}).get("content") or "")
    check("429 primary falls back to healthy model", st == 200 and "echo: fallback e2e" in content, f"{st} {raw[:400]}")

    st, hdrs, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "auth"}],
    }, headers={"Authorization": "Bearer mr-sk-invalid"})
    check("invalid gateway key rejected", st == 401, f"{st} {raw[:200]}")

    st, hdrs, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "nope", "messages": [{"role": "user", "content": "x"}],
    }, headers=gw_headers)
    check("unknown route is 404", st == 404, f"{st} {raw[:200]}")

    # --- Guard --------------------------------------------------------------
    guard_headers = {"Authorization": f"Bearer {guard_secret}", "User-Agent": "myrouter-e2e/guard"}
    st1, _, raw1 = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "guard 1"}],
    }, headers=guard_headers)
    st2, h2, raw2 = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "guard 2"}],
    }, headers=guard_headers)
    err2 = (as_json(raw2) or {}).get("error") or {}
    check("RPM policy allows first request", st1 == 200, f"{st1} {raw1[:200]}")
    check("RPM policy blocks second request 429", st2 == 429 and bool(h2.get("Retry-After")),
          f"{st2} retry={h2.get('Retry-After')} {raw2[:300]}")
    check("guard error names scope", err2.get("code") == "rate_limit_exceeded" and "api_key" in str(err2.get("message", "")),
          str(err2))

    # on_exceed=queue: second request waits for the bucket instead of 429.
    st, _, raw = request(sess, "POST", url("/admin/policies"), {
        "name": "E2E queue", "rpm": 60, "burst": 1, "queue_max_s": 3, "on_exceed": "queue",
    }, include_csrf=True)
    queue_policy = ((as_json(raw) or {}).get("data") or {}).get("id", "")
    st, _, raw = request(sess, "POST", url("/admin/api-keys"), {
        "name": "E2E queue key", "allowed_routes": ["coding"], "policy_id": queue_policy,
    }, include_csrf=True)
    queue_secret = (as_json(raw) or {}).get("secret", "")
    check("create queue policy + key", st == 201 and bool(queue_secret), f"{st}")
    qh = {"Authorization": f"Bearer {queue_secret}"}
    st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "queue 1"}],
    }, headers=qh)
    t0 = time.time()
    st2, _, raw2 = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "queue 2"}],
    }, headers=qh, timeout=15)
    elapsed = time.time() - t0
    check("on_exceed=queue waits instead of rejecting", st == 200 and st2 == 200 and elapsed >= 0.5,
          f"{st}/{st2} elapsed={elapsed:.2f}s {raw2[:250]}")

    # route whitelist per key
    st, _, raw = request(sess, "POST", url("/admin/api-keys"), {
        "name": "E2E locked", "allowed_routes": ["fallback-test"],
    }, include_csrf=True)
    locked_secret = (as_json(raw) or {}).get("secret", "")
    st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "locked"}],
    }, headers={"Authorization": f"Bearer {locked_secret}"})
    check("key route whitelist blocks other aliases", st == 403, f"{st} {raw[:250]}")

    # Editing a policy must work: the UI used to send `id` in the PATCH body,
    # which DisallowUnknownFields rejected with a confusing invalid_body.
    st, _, raw = request(sess, "POST", url("/admin/policies"), {
        "name": "E2E edit", "rpm": 5,
    }, include_csrf=True)
    edit_policy = ((as_json(raw) or {}).get("data") or {}).get("id", "")
    st, _, raw = request(sess, "PATCH", url(f"/admin/policies/{edit_policy}"), {
        "name": "E2E edit", "rpm": 7, "burst": 2,
    }, include_csrf=True)
    check("policy edit succeeds without id in body", st == 200 and ((as_json(raw) or {}).get("data") or {}).get("rpm") == 7,
          f"{st} {raw[:300]}")

    # Device block -> gateway 403 -> unblock -> gateway 200, and the block entry
    # must be removed (previously only the device flag was cleared).
    st, _, raw = request(sess, "GET", url("/admin/devices"))
    devs = (as_json(raw) or {}).get("data") or []
    if devs:
        dev_id = devs[0].get("id")
        st, _, raw = request(sess, "POST", url(f"/admin/devices/{dev_id}/block"), {"blocked": True}, include_csrf=True)
        check("block device", st == 200, f"{st} {raw[:200]}")
        st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
            "model": "coding", "messages": [{"role": "user", "content": "blocked device"}],
        }, headers=gw_headers)
        check("blocked device gets 403", st == 403, f"{st} {raw[:200]}")
        st, _, raw = request(sess, "POST", url(f"/admin/devices/{dev_id}/block"), {"blocked": False}, include_csrf=True)
        check("unblock device", st == 200, f"{st} {raw[:200]}")
        st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
            "model": "coding", "messages": [{"role": "user", "content": "after unblock"}],
        }, headers=gw_headers)
        check("traffic resumes after unblock", st == 200, f"{st} {raw[:250]}")
        st, _, raw = request(sess, "GET", url("/admin/blocked"))
        blocks = (as_json(raw) or {}).get("data") or []
        check("device block entry removed", all(b.get("value") != dev_id for b in blocks), f"{st} {raw[:250]}")
    else:
        check("device registered for block test", False, "no devices")

    # IP deny rule
    st, _, raw = request(sess, "POST", url("/admin/ip-rules"), {
        "cidr": "127.0.0.1/32", "action": "deny", "scope_type": "global", "note": "e2e",
    }, include_csrf=True)
    rule_id = ((as_json(raw) or {}).get("data") or {}).get("id", "")
    check("create deny IP rule", st == 201 and bool(rule_id), f"{st} {raw[:250]}")
    st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "denied"}],
    }, headers=gw_headers)
    check("denied IP gets 403", st == 403, f"{st} {raw[:250]}")
    if rule_id:
        st, _, raw = request(sess, "DELETE", url(f"/admin/ip-rules/{rule_id}"), include_csrf=True)
        check("delete IP rule", st == 200, f"{st} {raw[:200]}")
        st, _, raw = request(anon, "POST", url("/v1/chat/completions"), {
            "model": "coding", "messages": [{"role": "user", "content": "after unblock"}],
        }, headers=gw_headers)
        check("traffic resumes after rule removal", st == 200, f"{st} {raw[:250]}")

    # --- activity / usage / devices ----------------------------------------
    st, _, raw = request(sess, "GET", url("/admin/activity?limit=100"))
    acts = (as_json(raw) or {}).get("data") or []
    check("activity records gateway requests", st == 200 and len(acts) >= 4, f"{st} count={len(acts)}")
    trace_ok = False
    if acts:
        entry = next((a for a in acts if a.get("request_id") == first_request_id), acts[0])
        st2, _, raw2 = request(sess, "GET", url(f"/admin/activity/{entry.get('id')}"))
        detail = (as_json(raw2) or {}).get("data") or {}
        trace = detail.get("trace")
        trace_ok = st2 == 200 and isinstance(trace, list) and len(trace) >= 1
    check("activity detail exposes routing trace", trace_ok, "")

    st, _, raw = request(sess, "GET", url("/admin/usage?group_by=route&days=1"))
    usage = as_json(raw) or {}
    check("usage aggregates requests", st == 200 and len(usage.get("data") or []) >= 1,
          f"{st} {raw[:300]}")

    st, _, raw = request(sess, "GET", url("/admin/devices"))
    check("devices registered", st == 200 and len((as_json(raw) or {}).get("data") or []) >= 1, f"{st}")

    st, _, raw = request(sess, "GET", url("/admin/overview"))
    check("dashboard overview", st == 200, f"{st} {raw[:200]}")

    # --- CORS ---------------------------------------------------------------
    st, hdrs, raw = request(anon, "OPTIONS", url("/v1/chat/completions"),
                            headers={"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"})
    check("unknown CORS origin rejected on preflight", st == 403, f"{st} {raw[:200]}")
    st, hdrs, raw = request(anon, "POST", url("/v1/chat/completions"), {
        "model": "coding", "messages": [{"role": "user", "content": "cors"}],
    }, headers={**gw_headers, "Origin": "https://evil.example"})
    check("unknown origin gets no ACAO header", "Access-Control-Allow-Origin" not in hdrs, str(hdrs))

    # --- summary ------------------------------------------------------------
    passed = sum(1 for r in RESULTS if r["ok"])
    total = len(RESULTS)
    report = {
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%S%z", time.localtime(started)),
        "duration_s": round(time.time() - started, 2),
        "base_url": base,
        "fresh_instance": was_fresh,
        "passed": passed,
        "total": total,
        "ok": passed == total,
        "checks": RESULTS,
    }
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2, ensure_ascii=False)
    print(f"\n{passed}/{total} checks passed in {report['duration_s']}s -> {args.out}", flush=True)
    return 0 if passed == total else 1


if __name__ == "__main__":
    sys.exit(main())

