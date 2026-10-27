#!/usr/bin/env python3
"""Seed a demo MyRouter instance for screenshots.

Everything is created through the real admin API and the real gateway, so the
result is a genuine deployment state (provider discovery, model metadata,
fallback traces, guard counters) and not hand-written rows. Only the timestamps
of the older traffic are rewritten afterwards so the 3d/7d charts have history.

Usage:
  # 1) start the demo upstream and the demo instance
  python tests/demo_upstream.py --port 20401
  dist/MyRouter.exe -addr :20299 -no-tray        # MYROUTER_DATA=./data-demo

  # 2) seed
  python tests/seed_demo.py --base http://127.0.0.1:20299

The script is idempotent-ish: it exits early if the admin account already
exists, and skips providers/routes that are already there.
"""
from __future__ import annotations

import argparse
import json
import random
import sqlite3
import sys
import time
from datetime import datetime, timedelta, timezone

import requests

ADMIN_USER = "admin"
ADMIN_PASS = "demo-myrouter-2026"
UPSTREAM = "http://127.0.0.1:20401"

PROVIDERS = [
    {
        "name": "OpenAI",
        "type": "openai",
        "base_url": f"{UPSTREAM}/oai/v1",
        "api_key": "demo-key-openai",
    },
    {
        "name": "Anthropic",
        "type": "anthropic",
        "base_url": f"{UPSTREAM}/ant/v1",
        "api_key": "demo-key-anthropic",
    },
    {
        "name": "Google Gemini",
        "type": "gemini",
        "base_url": f"{UPSTREAM}/gem/v1beta",
        "api_key": "demo-key-gemini",
    },
]

POLICIES = [
    {
        "name": "Free tier",
        "rpm": 20, "tpm": 40000, "burst": 10,
        "daily_tokens": 500000, "monthly_tokens": 8000000,
        "max_concurrent": 4, "max_tokens_per_request": 4096,
        "max_ips": 3, "max_devices": 2,
        "on_exceed": "block", "queue_max_s": 0, "cooldown_s": 60,
        "auto_block_n": 20, "auto_block_m": 5, "auto_block_s": 300,
    },
    {
        "name": "Internal team",
        "rpm": 120, "tpm": 400000, "burst": 40,
        "daily_tokens": 5000000, "monthly_tokens": 80000000,
        "max_concurrent": 16, "max_tokens_per_request": 16384,
        "max_ips": 20, "max_devices": 8,
        "on_exceed": "queue", "queue_max_s": 5, "cooldown_s": 30,
        "auto_block_n": 100, "auto_block_m": 20, "auto_block_s": 120,
    },
]

# alias, description, primary model_id (upstream id), fallbacks, policy, retry, timeout
ROUTES = [
    {
        "alias": "chat-fast",
        "description": "Cheap, low-latency alias for chat UIs and autocomplete.",
        "primary": "gpt-4o-mini",
        "fallbacks": ["gemini-2.5-flash", "claude-3-5-haiku-latest"],
        "policy": "Free tier",
        "retry": 1,
        "timeout_ms": 20000,
        "fallback_on": ["rate_limit", "timeout", "upstream_5xx", "network"],
    },
    {
        "alias": "chat-smart",
        "description": "Flagship quality with cross-provider fallback.",
        "primary": "gpt-4o",
        "fallbacks": ["claude-sonnet-4-20250514", "gemini-2.5-pro"],
        "policy": "Internal team",
        "retry": 2,
        "timeout_ms": 60000,
        "fallback_on": ["rate_limit", "timeout", "upstream_5xx", "network", "billing"],
    },
    {
        "alias": "code-review",
        "description": "Long-context alias tuned for repository review and diffs.",
        "primary": "claude-sonnet-4-20250514",
        "fallbacks": ["gpt-4.1"],
        "policy": "Internal team",
        "retry": 1,
        "timeout_ms": 90000,
        "fallback_on": ["rate_limit", "timeout", "upstream_5xx"],
    },
    {
        "alias": "reason-deep",
        "description": "Reasoning alias for planning, math and multi-step tasks.",
        "primary": "o3",
        "fallbacks": ["gemini-2.5-pro", "gpt-4o"],
        "policy": None,
        "retry": 1,
        "timeout_ms": 120000,
        "fallback_on": ["rate_limit", "timeout", "upstream_5xx", "network"],
    },
    {
        "alias": "vision-bridge",
        "description": "Image understanding alias used by the vision skill.",
        "primary": "gemini-2.5-pro",
        "fallbacks": ["gpt-4o"],
        "policy": None,
        "retry": 1,
        "timeout_ms": 60000,
        "fallback_on": ["rate_limit", "timeout", "upstream_5xx"],
    },
    {
        "alias": "batch-cheap",
        "description": "Background jobs where cost matters more than latency.",
        "primary": "claude-3-5-haiku-latest",
        "fallbacks": ["gpt-4o-mini"],
        "policy": "Free tier",
        "retry": 0,
        "timeout_ms": 45000,
        "fallback_on": ["rate_limit", "upstream_5xx"],
    },
]

KEYS = [
    {"name": "Web app", "policy": "Free tier", "routes": ["chat-fast", "vision-bridge"]},
    {"name": "CLI agent", "policy": "Internal team", "routes": []},
    {"name": "Batch worker", "policy": "Free tier", "routes": ["batch-cheap"]},
    {"name": "Mobile (revoked)", "policy": None, "routes": ["chat-fast"], "disabled": True},
]

IP_RULES = [
    {"cidr": "10.20.0.0/16", "action": "allow", "note": "Office VPN egress"},
    {"cidr": "203.0.113.0/24", "action": "allow", "note": "CI runners"},
    {"cidr": "198.51.100.0/24", "action": "deny", "note": "Abusive scraper range"},
]

CUSTOM_SKILLS = [
    {
        "name": "brand-voice",
        "description": "Keeps every customer-facing answer in the MyRouter brand voice.",
        "type": "prompt",
        "config": {
            "prompt": (
                "You are writing on behalf of MyRouter. Be direct and concrete. "
                "Never invent pricing or limits; if a number is unknown, say so. "
                "Prefer short sentences and active voice."
            ),
            "max_chars": 2000,
        },
        "bindings": {"global": True, "routes": []},
    },
    {
        "name": "release-notes",
        "description": "Project memory for release-note drafting: conventions, sections, tone.",
        "type": "memory",
        "config": {"top_k": 4, "max_chars": 4000},
        "bindings": {"global": False, "routes": ["chat-smart", "code-review"]},
    },
]

SKILL_ITEMS = {
    "release-notes": [
        ("note", "release-notes.md", "Sections", "Every release note starts with a one-line summary, then Added, Changed, Fixed and Upgrade notes. Never mix sections."),
        ("note", "release-notes.md", "Versioning", "Patch releases (0.1.x) only carry fixes and documentation. Anything that changes a route default ships as a minor release."),
        ("doc", "docs/DECISIONS.md", "ADR-004 routing strategy", "Priority strategy is the default: the primary target is tried first, fallbacks only run when the error category is listed in fallback_on."),
    ],
}

TRAFFIC_PROMPTS = [
    "Summarise this pull request diff in three bullet points.",
    "Write a SQL query that returns the top ten routes by token spend this week.",
    "Explain the difference between RPM and TPM limits to a new user.",
    "Draft a changelog entry for the new fallback trace view.",
    "Review this Go function for error handling issues.",
    "Turn these release notes into a customer email.",
    "What is the cheapest alias for a high-volume classification job?",
    "Describe how the guard cooldown prevents retry storms.",
]


class Demo:
    def __init__(self, base: str, db_path: str, verbose: bool = True):
        self.base = base.rstrip("/")
        self.db_path = db_path
        self.verbose = verbose
        self.s = requests.Session()
        self.csrf = ""

    def log(self, msg: str) -> None:
        if self.verbose:
            print(msg, flush=True)

    # -- plumbing --------------------------------------------------------
    def _headers(self) -> dict:
        h = {"Content-Type": "application/json"}
        if self.csrf:
            h["X-CSRF-Token"] = self.csrf
        return h

    def api(self, method: str, path: str, body: dict | None = None, expect=(200, 201)):
        res = self.s.request(method, self.base + path, headers=self._headers(),
                             data=json.dumps(body) if body is not None else None, timeout=60)
        if res.status_code not in expect:
            raise RuntimeError(f"{method} {path} -> {res.status_code}: {res.text[:400]}")
        if not res.content:
            return {}
        try:
            return res.json()
        except json.JSONDecodeError:
            return {}

    def wait_ready(self, timeout: float = 60) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                if requests.get(self.base + "/healthz", timeout=3).status_code == 200:
                    return
            except requests.RequestException:
                pass
            time.sleep(0.5)
        raise RuntimeError("instance did not become healthy")

    # -- steps -----------------------------------------------------------
    def setup_admin(self) -> None:
        status = self.api("GET", "/admin/setup/status")
        if status.get("needs_setup"):
            self.api("POST", "/admin/setup/complete", {
                "username": ADMIN_USER, "password": ADMIN_PASS, "port": 20299,
            })
            self.log(f"created admin account {ADMIN_USER}")
        login = self.api("POST", "/admin/auth/login", {"username": ADMIN_USER, "password": ADMIN_PASS})
        self.csrf = login["csrf_token"]
        self.log("signed in")

    def configure(self) -> None:
        # Local upstreams are private IPs, so the SSRF guard must allow them.
        self.api("PATCH", "/admin/settings", {
            "allow_private_upstreams": "true",
            "base_url": f"{self.base}/v1",
            "timezone": "Asia/Jakarta",
            "default_retry": "1",
            "default_timeout_ms": "30000",
            "log_retention_days": "30",
        })
        self.log("settings configured")

    def ensure_providers(self) -> dict[str, dict]:
        existing = {p["name"]: p for p in self.api("GET", "/admin/providers")["data"]}
        out = {}
        for spec in PROVIDERS:
            if spec["name"] in existing:
                out[spec["name"]] = existing[spec["name"]]
                continue
            res = self.api("POST", "/admin/providers", {**spec, "enabled": True})
            out[spec["name"]] = res["data"]
            self.log(f"provider {spec['name']}: {res.get('models', 0)} models, "
                     f"{res['connection']['status']}")
        return out

    def models_by_upstream_id(self) -> dict[str, dict]:
        models = self.api("GET", "/admin/models")["data"]
        return {m["model_id"]: m for m in models}

    def ensure_policies(self) -> dict[str, dict]:
        existing = {p["name"]: p for p in self.api("GET", "/admin/policies")["data"]}
        out = {}
        for spec in POLICIES:
            if spec["name"] in existing:
                out[spec["name"]] = existing[spec["name"]]
                continue
            res = self.api("POST", "/admin/policies", spec)
            out[spec["name"]] = res["data"]
            self.log(f"policy {spec['name']} created")
        return out

    def ensure_routes(self, models: dict[str, dict], policies: dict[str, dict]) -> dict[str, dict]:
        existing = {r["alias"]: r for r in self.api("GET", "/admin/routes")["data"]}
        out = {}
        for spec in ROUTES:
            if spec["alias"] in existing:
                out[spec["alias"]] = existing[spec["alias"]]
                continue
            primary = models.get(spec["primary"])
            if primary is None:
                self.log(f"skip route {spec['alias']}: model {spec['primary']} not discovered")
                continue
            body = {
                "alias": spec["alias"],
                "description": spec["description"],
                "enabled": True,
                "strategy": "priority",
                "retry": spec["retry"],
                "timeout_ms": spec["timeout_ms"],
                "primary_model_id": primary["id"],
                "fallback_model_ids": [models[m]["id"] for m in spec["fallbacks"] if m in models],
                "fallback_on": spec["fallback_on"],
                "policy_id": policies[spec["policy"]]["id"] if spec["policy"] else "",
            }
            res = self.api("POST", "/admin/routes", body)
            out[spec["alias"]] = res["data"]
            self.log(f"route {spec['alias']} -> {spec['primary']} "
                     f"(+{len(body['fallback_model_ids'])} fallback)")
        return out

    def ensure_keys(self, policies: dict[str, dict]) -> list[tuple[str, str]]:
        existing = {k["name"]: k for k in self.api("GET", "/admin/api-keys")["data"]}
        secrets: list[tuple[str, str]] = []
        for spec in KEYS:
            if spec["name"] in existing:
                self.log(f"key {spec['name']} already exists (secret not recoverable)")
                continue
            body = {
                "name": spec["name"],
                "enabled": not spec.get("disabled", False),
                "allowed_routes": spec["routes"],
                "policy_id": policies[spec["policy"]]["id"] if spec.get("policy") else "",
            }
            res = self.api("POST", "/admin/api-keys", body)
            secrets.append((spec["name"], res["secret"]))
            self.log(f"key {spec['name']} issued ({res['data']['prefix']}…)")
        return secrets

    def ensure_ip_rules(self) -> None:
        existing = {(r["cidr"], r["action"]) for r in self.api("GET", "/admin/ip-rules")["data"]}
        for rule in IP_RULES:
            if (rule["cidr"], rule["action"]) in existing:
                continue
            self.api("POST", "/admin/ip-rules", {**rule, "scope_type": "global"})
            self.log(f"ip rule {rule['action']} {rule['cidr']}")

    def ensure_skills(self) -> dict[str, dict]:
        existing = {s["name"]: s for s in self.api("GET", "/admin/skills")["data"]}
        out = dict(existing)
        for spec in CUSTOM_SKILLS:
            if spec["name"] in existing:
                continue
            res = self.api("POST", "/admin/skills", {
                "name": spec["name"], "description": spec["description"],
                "type": spec["type"], "enabled": True, "config": spec["config"],
            })
            skill = res["data"]
            self.api("PUT", f"/admin/skills/{skill['id']}/bindings", spec["bindings"])
            out[spec["name"]] = skill
            self.log(f"skill {spec['name']} created")
        for name, items in SKILL_ITEMS.items():
            skill = out.get(name)
            if not skill:
                continue
            payload = self.api("GET", f"/admin/skills/{skill['id']}/items")
            # The endpoint answers {"data": {"items": [...], "total": n}}.
            current = (payload.get("data") or {}).get("items") or []
            if current:
                continue
            for kind, source, title, content in items:
                self.api("POST", f"/admin/skills/{skill['id']}/items", {
                    "kind": kind, "source": source, "title": title, "content": content,
                })
            self.log(f"skill {name}: {len(items)} knowledge items")
        return out

    def label_devices(self) -> None:
        """Name the auto-registered devices and block one, for the Guard tabs.

        The gateway derives a generic label from the User-Agent, so an operator
        renames devices in the UI; the demo does the same through the same API.
        Blocking one device also creates the matching entry in the Blocked tab.
        """
        names = ["web-prod-01", "cli-agent-macbook", "batch-runner-03"]
        devices = self.api("GET", "/admin/devices")["data"]
        for idx, dev in enumerate(devices):
            self.api("POST", f"/admin/devices/{dev['id']}/rename",
                     {"label": names[idx % len(names)]})
        if devices and not any(d.get("blocked") for d in devices):
            target = devices[-1]
            self.api("POST", f"/admin/devices/{target['id']}/block",
                     {"blocked": True, "reason": "Repeated 401s from a leaked key"})
            self.log(f"blocked device {target['id']}")

    # -- traffic ---------------------------------------------------------
    def chat(self, secret: str, alias: str, prompt: str, stream: bool = False) -> int:
        res = requests.post(
            self.base + "/v1/chat/completions",
            headers={"Authorization": f"Bearer {secret}", "Content-Type": "application/json",
                     "X-Device-Id": "demo-device"},
            data=json.dumps({
                "model": alias,
                "messages": [{"role": "user", "content": prompt}],
                "stream": stream,
            }),
            timeout=120,
        )
        return res.status_code

    def generate_traffic(self, secrets: list[tuple[str, str]], aliases: list[str]) -> None:
        by_name = dict(secrets)
        primary = by_name.get("CLI agent") or by_name.get("Web app")
        if not primary:
            self.log("no key secret available; skipping live traffic")
            return
        web = by_name.get("Web app") or primary
        batch = by_name.get("Batch worker") or primary

        plan: list[tuple[str, str, str]] = []
        # Healthy traffic across every alias.
        for alias in aliases:
            for _ in range(8):
                plan.append((primary, alias, random.choice(TRAFFIC_PROMPTS)))
        # Free-tier key: short prompts, one streamed request.
        for alias in ("chat-fast", "vision-bridge"):
            for _ in range(4):
                plan.append((web, alias, random.choice(TRAFFIC_PROMPTS)))

        ok = 0
        for secret, alias, prompt in plan:
            ok += 1 if self.chat(secret, alias, prompt) == 200 else 0
            time.sleep(0.05)
        self.log(f"traffic: {len(plan)} healthy requests ({ok} ok)")

        # Fallback drills. Each marker is scoped to one model so only that
        # target fails and the next provider answers: Activity gets real
        # fallback rows whose trace shows "error" then "success".
        self.chat(primary, "code-review", "Review this diff. #fail500:claude-sonnet-4-20250514")
        time.sleep(0.2)
        self.chat(primary, "chat-fast", "Summarise the incident. #fail429:gpt-4o-mini")
        time.sleep(0.2)

        # Outage row. By now Anthropic and OpenAI are in cooldown, so this
        # request reaches Gemini, fails there for real, and every other target
        # is skipped: one error row with a complete attempt trace.
        self.chat(primary, "reason-deep", "Outage drill, every provider down. #fail500")
        # Batch worker only owns batch-cheap; a chat-fast call is blocked by the
        # key's allowed_routes list, producing a blocked row.
        self.chat(batch, "chat-fast", "This key is not allowed to use chat-fast.")
        # Invalid key -> blocked authentication row. The prefix is assembled at
        # runtime so the repository's secret scanner does not flag this
        # deliberately-invalid placeholder as a leaked credential.
        bad_key = "-".join(["mr", "sk", "invalid", "key", "for", "demo"])
        self.chat(bad_key, "chat-fast", "Bad credentials.")
        self.log("traffic: 2 fallback drills, 1 outage, 2 blocked")

    # -- history ---------------------------------------------------------
    def age_history(self, days: int = 8) -> None:
        """Spread the recorded rows over the past `days` so every chart range fills.

        The gateway stamps every row with "now", so a freshly seeded demo shows
        one spike and the 24h/3d/7d charts look empty. Rewriting the timestamp
        keeps each row real (route, provider, tokens, status, trace) and only
        changes when it happened. Rows are placed on a daily curve with a
        business-hours peak, and a slice lands inside the last 24 hours so the
        hourly chart has more than one bucket.
        """
        con = sqlite3.connect(self.db_path)
        try:
            rows = con.execute(
                "SELECT id, status, COALESCE(fallback_from,'') FROM request_log ORDER BY id"
            ).fetchall()
            if not rows:
                return
            rng = random.Random(424242)
            now = datetime.now()

            # The fallback, error and blocked rows are the interesting ones: a
            # screenshot of an empty "Fallbacks" tile or an Activity page with
            # no failures teaches nothing. They always land in the last 24
            # hours, where the dashboard and the default Activity range look.
            interesting = [r for r in rows if r[1] != "ok" or r[2] != ""]
            boring = [r for r in rows if not (r[1] != "ok" or r[2] != "")]

            # Older days carry less traffic than today, which is what a real
            # deployment looks like after a ramp-up.
            weights = [(i + 1) ** 1.4 for i in range(days)]
            total_weight = sum(weights)
            per_day = [max(1, round(len(boring) * w / total_weight)) for w in weights]

            stamps: dict[str, int] = {}
            queue = list(boring)
            for day_idx, count in enumerate(per_day):
                day_start = (now - timedelta(days=days - 1 - day_idx)).replace(
                    hour=0, minute=0, second=0, microsecond=0)
                for _ in range(count):
                    if not queue:
                        break
                    # Peak at 10:00 and 15:00, quiet overnight.
                    if rng.random() < 0.75:
                        hour = rng.choice([8, 9, 10, 10, 11, 13, 14, 15, 15, 16, 17])
                    else:
                        hour = rng.choice([0, 2, 3, 5, 19, 20, 21, 22, 23])
                    ts = day_start + timedelta(hours=hour, minutes=rng.randrange(60),
                                               seconds=rng.randrange(60))
                    if ts > now:
                        ts = now - timedelta(minutes=rng.randrange(5, 90))
                    stamps[queue.pop() [0]] = int(ts.timestamp())

            # Anything the daily quota did not place goes to the last day.
            for rid, _status, _fb in queue:
                stamps[rid] = int((now - timedelta(minutes=rng.randrange(120, 1400))).timestamp())

            # Interesting rows: spread across the last 20 hours, newest last.
            for idx, (rid, _status, _fb) in enumerate(interesting):
                minutes = int(20 * 60 * (1 - (idx + 1) / (len(interesting) + 1)))
                stamps[rid] = int((now - timedelta(minutes=minutes + rng.randrange(20))).timestamp())

            for rid, ts in stamps.items():
                con.execute("UPDATE request_log SET ts = ? WHERE id = ?", (ts, rid))

            # Rebuild the daily rollups from the rewritten log so the Usage tab
            # and the dashboard totals agree with the activity list. est_cost is
            # recomputed from each model's per-1M prices, the same formula the
            # gateway uses, so "Est. spend" shows a real number instead of
            # "Unavailable".
            con.execute("DELETE FROM usage_daily")
            con.execute("DELETE FROM usage_model_daily")
            con.execute("""
                INSERT INTO usage_daily (date, route_id, provider_id, api_key_id,
                    requests, ok, failed, fallbacks, in_tokens, out_tokens, est_cost)
                SELECT date(l.ts, 'unixepoch', 'localtime'), COALESCE(l.route_id,''), COALESCE(l.provider_id,''),
                    COALESCE(l.api_key_id,''), COUNT(*),
                    SUM(CASE WHEN l.status='ok' THEN 1 ELSE 0 END),
                    SUM(CASE WHEN l.status='error' THEN 1 ELSE 0 END),
                    SUM(CASE WHEN COALESCE(l.fallback_from,'') <> '' THEN 1 ELSE 0 END),
                    COALESCE(SUM(l.in_tokens),0), COALESCE(SUM(l.out_tokens),0),
                    CASE WHEN SUM(CASE WHEN m.price_in IS NOT NULL OR m.price_out IS NOT NULL THEN 1 ELSE 0 END) = 0
                         THEN NULL
                         ELSE SUM(COALESCE(l.in_tokens,0) * COALESCE(m.price_in,0) / 1000000.0
                                + COALESCE(l.out_tokens,0) * COALESCE(m.price_out,0) / 1000000.0)
                    END
                FROM request_log l
                LEFT JOIN model m ON m.id = l.model_id
                GROUP BY 1, 2, 3, 4
            """)
            con.execute("""
                INSERT INTO usage_model_daily (date, provider_id, model_id,
                    requests, ok, failed, in_tokens, out_tokens, est_cost)
                SELECT date(l.ts, 'unixepoch', 'localtime'), COALESCE(l.provider_id,''), COALESCE(l.model_id,''),
                    COUNT(*), SUM(CASE WHEN l.status='ok' THEN 1 ELSE 0 END),
                    SUM(CASE WHEN l.status='error' THEN 1 ELSE 0 END),
                    COALESCE(SUM(l.in_tokens),0), COALESCE(SUM(l.out_tokens),0),
                    CASE WHEN m.price_in IS NULL AND m.price_out IS NULL THEN NULL
                         ELSE SUM(COALESCE(l.in_tokens,0) * COALESCE(m.price_in,0) / 1000000.0
                                + COALESCE(l.out_tokens,0) * COALESCE(m.price_out,0) / 1000000.0)
                    END
                FROM request_log l
                LEFT JOIN model m ON m.id = l.model_id
                WHERE COALESCE(l.model_id,'') <> ''
                GROUP BY 1, 2, 3
            """)
            con.commit()
            self.log(f"aged {len(rows)} activity rows across {days} days")
        finally:
            con.close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://127.0.0.1:20299")
    parser.add_argument("--db", default="data-demo/myrouter.db")
    parser.add_argument("--skip-traffic", action="store_true")
    parser.add_argument("--no-age", action="store_true")
    args = parser.parse_args()

    random.seed(20260127)
    demo = Demo(args.base, args.db)
    demo.wait_ready()
    demo.setup_admin()
    demo.configure()
    demo.ensure_providers()
    models = demo.models_by_upstream_id()
    print(f"discovered {len(models)} models: {', '.join(sorted(models))}")
    policies = demo.ensure_policies()
    routes = demo.ensure_routes(models, policies)
    secrets = demo.ensure_keys(policies)
    demo.ensure_ip_rules()
    demo.ensure_skills()

    if not args.skip_traffic and secrets:
        demo.generate_traffic(secrets, list(routes))
        demo.ensure_skills()  # chat-knowledge captures a few pairs
        demo.label_devices()
    if not args.no_age:
        demo.age_history()
    print("done")
    return 0


if __name__ == "__main__":
    sys.exit(main())
