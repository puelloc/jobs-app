"""block_detect.py: recognise a blocked, throttled or challenged page.

Why this exists: the pipeline reads a page, finds nothing, and records "no postings". A CAPTCHA, a
Cloudflare interstitial, an "Access Denied" page, a login wall and a company that genuinely has no
openings all look identical from there - and they call for completely different responses. Retrying
later, slowing down, taking a different route, or accepting that there is nothing to find.

The distinction also has a correctness consequence upstream: a blocked company must not be cached as
"has no remote roles", because that verdict is reused for days and a transient block would be frozen
into a fact.

Pure functions over a status code, a title and body text, so this is testable with fixtures and cheap
enough to run on every page.
"""

from __future__ import annotations

from typing import NamedTuple

# How much of a page to scan. A block page announces itself in the first screenful; scanning an entire
# 2MB SPA payload for the word "captcha" would cost more than it finds.
SCAN_LIMIT = 60_000

# Ordered by specificity: a reCAPTCHA page also says "verify", but "captcha" is the more useful reason.
CAPTCHA_MARKERS = (
    "recaptcha",
    "hcaptcha",
    "turnstile",
    "g-recaptcha",
    "captcha",
    "verify you are human",
    "verify you are a human",
    "are you a robot",
    "prove you are not a robot",
    "complete the security check",
)
# Deliberately NOT matched: "human verification" on its own. A job ad for a security or identity team
# says "human verification systems" in prose, and a false positive on a posting is noise while a false
# positive on a listings page stops a company being scraped at all.

CHALLENGE_MARKERS = (
    "cf-chl",
    "cf_chl",
    "cloudflare",
    "just a moment",
    "checking your browser",
    "enable javascript and cookies to continue",
    "ddos protection by",
    "attention required!",
    "akamai bot manager",
    "incapsula",
    "imperva",
    "px-captcha",
)

BLOCKED_MARKERS = (
    "access denied",
    "you have been blocked",
    "request blocked",
    "blocked by",
    "unusual traffic",
    "automated queries",
    "bot detected",
    "denied by",
    "forbidden",
    "not permitted to access",
    "your request has been blocked",
)

LOGIN_MARKERS = (
    "sign in to continue",
    "log in to continue",
    "please log in",
    "please sign in",
    "authentication required",
    "session expired",
    "create an account to view",
    "register to view",
)

# Only the statuses that reliably mean "you were not served the content".
STATUS_REASONS = {
    401: "login_required",
    403: "forbidden",
    429: "rate_limited",
    503: "unavailable",
}


class Block(NamedTuple):
    blocked: bool
    # "" when not blocked, otherwise the most specific reason found.
    reason: str
    # The text that matched, so a log line explains itself without the page.
    evidence: str

    @property
    def is_block(self) -> bool:
        return self.blocked


NOT_BLOCKED = Block(False, "", "")


def classify(status: int | None = None, title: str = "", text: str = "") -> Block:
    """Decide whether this response is a block rather than content.

    Status first: an HTTP 403 with an unhelpful body is still a block. Then the page's own words, most
    specific first, because a challenge page often returns 200.
    """
    if status in STATUS_REASONS:
        return Block(True, STATUS_REASONS[status], f"HTTP {status}")

    haystack = f"{title}\n{text[:SCAN_LIMIT]}".lower()
    for reason, markers in (
        ("captcha", CAPTCHA_MARKERS),
        ("challenge", CHALLENGE_MARKERS),
        ("blocked", BLOCKED_MARKERS),
        ("login_required", LOGIN_MARKERS),
    ):
        for marker in markers:
            index = haystack.find(marker)
            if index >= 0:
                return Block(True, reason, _snippet(haystack, index))
    return NOT_BLOCKED


def _snippet(haystack: str, index: int, width: int = 60) -> str:
    start = max(0, index - 20)
    return " ".join(haystack[start:index + width].split())
