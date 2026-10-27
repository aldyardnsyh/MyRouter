#!/usr/bin/env python3
"""Capture the README screenshots from a running demo instance.

Light mode only. 1440x900 CSS pixels at 1.5x DPR, which is the size the
existing assets/screenshots set uses (2160x1350), so new and old images sit in
the README without a resolution jump. Every page is captured, and pages that
have internal menus/tabs are captured once per tab so the README can show what
each section actually looks like.

Usage:
  python tests/capture_screenshots.py --base http://127.0.0.1:20299 \
      --out assets/screenshots

Requires the demo instance from tests/seed_demo.py and a Playwright Chromium.
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from playwright.sync_api import Page, sync_playwright

ADMIN_USER = "admin"
ADMIN_PASS = "demo-myrouter-2026"


class Shooter:
    def __init__(self, page: Page, base: str, out: Path):
        self.page = page
        self.base = base.rstrip("/")
        self.out = out
        self.out.mkdir(parents=True, exist_ok=True)

    # -- helpers ---------------------------------------------------------
    def login_page(self) -> None:
        # Captured first, in a clean context, so no session cookie is present.
        self.page.goto(f"{self.base}/login", wait_until="networkidle")
        self.settle(400)
        self.shot("login")

    def login(self) -> None:
        if self.page.locator("input#username").count() == 0:
            self.page.goto(f"{self.base}/login", wait_until="networkidle")
        self.page.fill("input#username", ADMIN_USER)
        self.page.fill("input#password", ADMIN_PASS)
        self.page.click("button[type=submit]")
        self.page.wait_for_url(lambda url: "/login" not in url, timeout=20000)
        self.page.wait_for_load_state("networkidle")
        # Light mode is the default, but pin it so a stray localStorage value
        # from an earlier run cannot produce a dark screenshot.
        self.page.evaluate("localStorage.setItem('myrouter-theme','light')")
        self.page.reload(wait_until="networkidle")

    def settle(self, ms: int = 700) -> None:
        """Wait for data fetches, charts and fonts to finish before shooting."""
        self.page.wait_for_load_state("networkidle")
        self.page.wait_for_timeout(ms)

    def shot(self, name: str) -> None:
        path = self.out / f"{name}.png"
        self.page.screenshot(path=str(path), full_page=False)
        print(f"  {name}.png")

    def go(self, path: str) -> None:
        self.page.goto(f"{self.base}{path}", wait_until="networkidle")
        self.settle()

    def tab(self, label: str, index: int = 0) -> None:
        """Click a tab/segmented button by visible text."""
        btn = self.page.get_by_role("tab", name=label, exact=False).nth(index)
        btn.click()
        self.settle(500)

    def button(self, label: str) -> None:
        self.page.get_by_role("button", name=label, exact=False).first.click()
        self.settle(500)

    # -- pages -----------------------------------------------------------
    def dashboard(self) -> None:
        self.go("/")
        self.shot("dashboard")
        # Chart metric switcher: Requests / Tokens / Latency.
        for metric in ("Tokens", "Latency"):
            self.tab(metric)
            self.shot(f"dashboard-{metric.lower()}")
        self.tab("Requests")
        # Chart range switcher: 24h / 3d / 7d / 30d.
        for rng in ("3d", "7d", "30d"):
            self.page.get_by_role("tab", name=rng, exact=True).click()
            self.settle(900)
            self.shot(f"dashboard-range-{rng}")

    def providers(self) -> None:
        self.go("/providers")
        self.shot("providers")
        # Provider detail: Overview / Models / Guard Policy.
        link = self.page.get_by_role("link", name="OpenAI", exact=False)
        if link.count() == 0:
            link = self.page.locator("table tbody tr").first
        link.first.click()
        self.settle(900)
        self.shot("provider-detail")
        for label in ("Models", "Guard Policy"):
            self.tab(label)
            self.shot(f"provider-detail-{label.lower().replace(' ', '-')}")

    def models(self) -> None:
        self.go("/models")
        self.shot("models")

    def routes(self) -> None:
        self.go("/routes")
        self.shot("routes")

    def skills(self) -> None:
        self.go("/skills")
        self.shot("skills")
        # Open the knowledge drawer of the skill that has items.
        card = self.page.get_by_text("release-notes", exact=False).first
        if card.count() > 0:
            parent = card.locator("xpath=ancestor::div[contains(@class,'rounded-card')][1]")
            kn = parent.get_by_role("button", name="Knowledge", exact=False)
            if kn.count() > 0:
                kn.first.click()
                self.settle(700)
                self.shot("skills-knowledge")

    def api_keys(self) -> None:
        self.go("/api-keys")
        self.shot("api-keys")

    def guard(self) -> None:
        self.go("/guard")
        self.shot("guard")
        for label, slug in (("Policies", "policies"), ("IP Rules", "ip-rules"),
                            ("Devices", "devices"), ("Blocked", "blocked")):
            self.tab(label)
            self.shot(f"guard-{slug}")

    def activity(self) -> None:
        self.go("/activity")
        self.shot("activity")
        self.tab("Usage")
        self.shot("activity-usage")
        # Detail drawer of a request that used a fallback, so the trace is visible.
        self.tab("Requests")
        row = self.page.locator("table tbody tr").first
        if row.count() > 0:
            row.click()
            self.settle(700)
            self.shot("activity-detail")
            close = self.page.get_by_role("button", name="Close", exact=False)
            if close.count() > 0:
                close.first.click()
                self.settle(300)

    def playground(self) -> None:
        self.go("/playground")
        self.shot("playground")

    def settings(self) -> None:
        self.go("/settings")
        self.shot("settings")
        for label, slug in (("API", "api"), ("Security", "security"),
                            ("Routing", "routing"), ("Guard defaults", "guard"),
                            ("Storage", "storage"), ("System", "system")):
            self.page.get_by_role("button", name=label, exact=True).first.click()
            self.settle(400)
            self.shot(f"settings-{slug}")

    def mobile(self) -> None:
        self.page.set_viewport_size({"width": 390, "height": 844})
        self.go("/")
        self.shot("dashboard-mobile")
        self.page.set_viewport_size({"width": 1440, "height": 900})


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://127.0.0.1:20299")
    parser.add_argument("--out", default="assets/screenshots")
    parser.add_argument("--only", default="", help="comma-separated page names")
    args = parser.parse_args()

    out = Path(args.out)
    only = {s.strip() for s in args.only.split(",") if s.strip()}

    with sync_playwright() as p:
        browser = p.chromium.launch()
        context = browser.new_context(
            viewport={"width": 1440, "height": 900},
            device_scale_factor=1.5,
            color_scheme="light",
            locale="en-US",
            timezone_id="Asia/Jakarta",
        )
        page = context.new_page()
        s = Shooter(page, args.base, out)

        s.login_page()
        s.login()

        steps = [
            ("dashboard", s.dashboard),
            ("providers", s.providers),
            ("models", s.models),
            ("routes", s.routes),
            ("skills", s.skills),
            ("api-keys", s.api_keys),
            ("guard", s.guard),
            ("activity", s.activity),
            ("playground", s.playground),
            ("settings", s.settings),
            ("mobile", s.mobile),
        ]
        for name, fn in steps:
            if only and name not in only:
                continue
            print(f"{name}:")
            fn()

        browser.close()
    print(f"\nscreenshots written to {out.resolve()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
