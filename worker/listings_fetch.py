#!/usr/bin/env python3
"""listings_fetch.py: Step B — turn a filtered careers-listings URL into job records.

Given the *results* URL a browser-use agent found (e.g. the Twilio Eightfold search URL), render it,
extract the individual job-posting URLs + titles, then render each posting and pull its full
description plus the board's embedded structured JSON (`application/ld+json` JobPosting). Output one
JSON array of job records.

No LLM and no DB writes here: this is the deterministic fetch half of the flow. The Go side owns
storage (job_listings upsert). It uses Playwright's own launcher (the `render` path, which works in
this sandbox), so there is no browser-use import and none of its config-dir / CDP / sandbox traps.

Usage:
    ./.venv-browser/bin/python worker/listings_fetch.py \
        --url "https://jobs.twilio.com/careers?query=software+engineer&locationType=Remote" \
        --max-jobs 5
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import time
from urllib.parse import urlparse

# Path tokens that identify an individual posting (vs nav/footer links). Kept broad on purpose:
# the caller can tighten with --link-pattern once a vendor's shape is known.
JOB_PATH_TOKENS = (
    "/job/", "/jobs/", "/posting/", "/postings/", "/opening/", "/openings/",
    "/position/", "/positions/", "/role/", "/vacancy/", "/detail/",
    "gh_jid", "job_id", "jobId",
)

DEFAULT_MAX_BODY_BYTES = 200_000


def _truncate(text: str, max_bytes: int) -> str:
    raw = text.encode("utf-8", "replace")
    if len(raw) <= max_bytes:
        return text
    return raw[:max_bytes].decode("utf-8", "ignore")


def _looks_like_posting(href: str, text: str) -> bool:
    if not href or href.startswith(("javascript:", "mailto:", "tel:", "#")):
        return False
    if not text.strip():
        return False
    path = urlparse(href).path.lower()
    return any(tok in path for tok in JOB_PATH_TOKENS)


def _strip_tags(html: str) -> str:
    """Light HTML -> text, enough for a readable description fallback."""
    text = re.sub(r"(?is)<(script|style)[^>]*>.*?</\1>", " ", html)
    text = re.sub(r"(?s)<[^>]+>", " ", text)
    text = re.sub(r"&nbsp;", " ", text)
    text = re.sub(r"\s+", " ", text)
    return text.strip()


_COUNTRY_CODES = {
    "united states": "US", "usa": "US", "united states of america": "US",
    "canada": "CA", "united kingdom": "GB", "uk": "GB", "india": "IN",
    "germany": "DE", "france": "FR", "spain": "ES", "mexico": "MX",
    "brazil": "BR", "australia": "AU", "singapore": "SG", "japan": "JP",
    "netherlands": "NL", "ireland": "IE", "poland": "PL", "portugal": "PT",
    "switzerland": "CH", "italy": "IT", "sweden": "SE", "denmark": "DK",
    "finland": "FI", "norway": "NO", "belgium": "BE", "austria": "AT",
}


def _as_list(value):
    if value is None:
        return []
    if isinstance(value, list):
        return value
    return [value]


def _country_code(value: str) -> str:
    """Map a country name or ISO code to its two-letter code, or "" when unrecognised."""
    v = value.strip()
    upper = v.upper()
    if len(upper) == 2 and upper.isalpha():
        return upper
    return _COUNTRY_CODES.get(v.lower(), "")


def _country_name_or_code(value) -> str:
    """Normalise a country field that may be a string code, a name, or a {"name": ...} dict."""
    if isinstance(value, dict):
        value = value.get("name") or ""
    return str(value or "").strip()


def _classify_location(posting: dict) -> dict:
    """Derive country / is_us / location_text from a JobPosting ld+json.

    The deterministic fix for the location leak: the browser-use agent applies only a coarse filter,
    and this reads the structured country out of the embedded JSON so the caller can keep exactly the
    US postings. The "remote" half is the caller's concern - the flow only extracts from a
    remote-filtered listings URL, so it does not try to guess remote from the posting.
    """
    countries: list[str] = []
    parts: list[str] = []

    # Greenhouse uses applicantLocationRequirements: a list of Country objects.
    for req in _as_list(posting.get("applicantLocationRequirements")):
        if not isinstance(req, dict):
            continue
        name = _country_name_or_code(req.get("name"))
        if name:
            parts.append(name)
            if code := _country_code(name):
                countries.append(code)

    # schema.org jobLocation: one or more Place objects carrying an address.
    for loc in _as_list(posting.get("jobLocation")):
        if not isinstance(loc, dict):
            continue
        addr = loc.get("address") or {}
        if not isinstance(addr, dict):
            continue
        country = _country_name_or_code(addr.get("addressCountry"))
        region = _country_name_or_code(addr.get("addressRegion"))
        locality = _country_name_or_code(addr.get("addressLocality"))
        if country:
            parts.append(country)
            if code := _country_code(country):
                countries.append(code)
        if region:
            parts.append(region)
        if locality:
            parts.append(locality)

    seen: set[str] = set()
    ordered: list[str] = []
    for p in parts:
        if p and p not in seen:
            seen.add(p)
            ordered.append(p)

    us = "US" in countries
    if us:
        country = "US"
    elif len(countries) == 1:
        country = countries[0]
    else:
        country = ""

    return {
        "country": country or None,
        "is_us": us if countries else None,
        "location_text": ", ".join(ordered) or None,
    }


def fetch(listings_url: str, max_jobs: int, max_body_bytes: int, timeout_s: int,
          link_pattern: str | None) -> dict:
    from playwright.sync_api import TimeoutError as PlaywrightTimeoutError
    from playwright.sync_api import sync_playwright

    timeout_ms = max(1000, int(timeout_s * 1000))
    link_re = re.compile(link_pattern) if link_pattern else None
    started = time.monotonic()

    jobs: list[dict] = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            page = browser.new_context().new_page()
            page.set_default_timeout(timeout_ms)

            page.goto(listings_url, wait_until="domcontentloaded", timeout=timeout_ms)
            try:
                page.wait_for_load_state("networkidle", timeout=min(8000, timeout_ms))
            except PlaywrightTimeoutError:
                pass

            anchors = page.evaluate(
                "() => Array.from(document.querySelectorAll('a[href]')).map(a => "
                "({href: a.href, text: (a.innerText || a.getAttribute('aria-label') || '').trim()}))"
            )

            picked: list[dict] = []
            seen: set[str] = set()
            for a in anchors:
                href, text = a["href"], a["text"]
                ok = link_re.search(href) if link_re else _looks_like_posting(href, text)
                if ok and href not in seen:
                    seen.add(href)
                    picked.append({"url": href, "title": text})
                if len(picked) >= max_jobs:
                    break

            for p in picked:
                rec = {"url": p["url"], "title": p["title"], "description": None, "raw_data": None}
                try:
                    page.goto(p["url"], wait_until="domcontentloaded", timeout=timeout_ms)
                    try:
                        page.wait_for_load_state("networkidle", timeout=min(8000, timeout_ms))
                    except PlaywrightTimeoutError:
                        pass

                    # Embedded JobPosting JSON is the richest source (title/description/location/date).
                    ld = page.evaluate(
                        "() => Array.from(document.querySelectorAll('script[type=\"application/ld+json\"]'))"
                        ".map(s => s.textContent)"
                    )
                    posting = None
                    for raw in ld:
                        try:
                            data = json.loads(raw)
                        except Exception:
                            continue
                        for item in (data if isinstance(data, list) else [data]):
                            if isinstance(item, dict) and (
                                item.get("@type") == "JobPosting" or "description" in item
                            ):
                                posting = item
                                break
                        if posting:
                            break

                    if posting:
                        rec["raw_data"] = json.dumps(posting, ensure_ascii=False)
                        desc = posting.get("description") or ""
                        rec["description"] = _truncate(_strip_tags(desc), max_body_bytes)
                        if posting.get("title"):
                            rec["title"] = posting["title"]
                        rec.update(_classify_location(posting))
                    else:
                        body = page.evaluate("() => document.body.innerText")
                        rec["description"] = _truncate(body, max_body_bytes)
                    rec["final_url"] = page.url
                except Exception as exc:  # noqa: BLE001 - a dead posting is a record, not a crash
                    rec["error"] = f"{type(exc).__name__}: {exc}"
                jobs.append(rec)
        finally:
            browser.close()

    return {
        "listings_url": listings_url,
        "found": len(jobs),
        "elapsed_sec": round(time.monotonic() - started, 1),
        "jobs": jobs,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--url", required=True, help="filtered listings/search-results URL")
    ap.add_argument("--max-jobs", type=int, default=25)
    ap.add_argument("--max-body-bytes", type=int, default=DEFAULT_MAX_BODY_BYTES)
    ap.add_argument("--timeout", type=int, default=45, help="per-page seconds")
    ap.add_argument("--link-pattern", default=None,
                    help="optional regex to select posting links (overrides the heuristic)")
    ap.add_argument("--out", default=None, help="write JSON to this file instead of stdout")
    args = ap.parse_args()

    result = fetch(args.url, args.max_jobs, args.max_body_bytes, args.timeout, args.link_pattern)

    payload = json.dumps(result, ensure_ascii=False, indent=2)
    if args.out:
        with open(args.out, "w", encoding="utf-8") as f:
            f.write(payload + "\n")
        print(f"wrote {args.out}", file=sys.stderr)
    else:
        print(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
