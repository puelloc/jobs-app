"""Tests for block_detect.

The cases are the ones actually worth telling apart, written as the page text that would be seen:
a genuine empty board must NOT be flagged, because a false positive here stops a company from being
scraped at all.
"""

from __future__ import annotations

import sys
import unittest

sys.path.insert(0, __file__.rsplit("/", 1)[0])

from block_detect import classify  # noqa: E402


class TestStatuses(unittest.TestCase):
    def test_known_statuses_map_to_reasons(self) -> None:
        for status, reason in ((401, "login_required"), (403, "forbidden"), (429, "rate_limited"), (503, "unavailable")):
            got = classify(status=status)
            self.assertTrue(got.blocked, f"HTTP {status} should be a block")
            self.assertEqual(got.reason, reason)
            self.assertEqual(got.evidence, f"HTTP {status}")

    def test_ordinary_statuses_are_not_blocks(self) -> None:
        for status in (200, 204, 301, 302, 404, 500, None):
            self.assertFalse(classify(status=status).blocked, f"HTTP {status} should not be a block")


class TestPageText(unittest.TestCase):
    def test_captcha_variants(self) -> None:
        for text in ("Please complete the reCAPTCHA to continue",
                     "Verify you are human by completing the action below",
                     "g-recaptcha-response",
                     "Are you a robot?"):
            got = classify(status=200, text=text)
            self.assertEqual(got.reason, "captcha", text)

    def test_cloudflare_and_cdn_challenges(self) -> None:
        for text in ("Just a moment...", "Checking your browser before accessing",
                     "Enable JavaScript and cookies to continue", "Attention Required! | Cloudflare"):
            got = classify(status=200, text=text)
            self.assertEqual(got.reason, "challenge", text)

    def test_generic_blocks(self) -> None:
        for text in ("Access Denied", "You have been blocked", "We detected unusual traffic from your network"):
            got = classify(status=200, text=text)
            self.assertEqual(got.reason, "blocked", text)

    def test_login_walls(self) -> None:
        got = classify(status=200, text="Please log in to continue to your account")
        self.assertEqual(got.reason, "login_required")

    def test_captcha_wins_over_a_generic_verify_word(self) -> None:
        # Both a captcha and a challenge marker present: the more specific one is reported.
        got = classify(status=200, text="Checking your browser... please complete the captcha")
        self.assertEqual(got.reason, "captcha")

    def test_evidence_is_a_readable_snippet(self) -> None:
        got = classify(status=200, text="x" * 500 + " Access Denied " + "y" * 500)
        self.assertIn("access denied", got.evidence)
        self.assertLess(len(got.evidence), 100)


class TestNoFalsePositives(unittest.TestCase):
    """A false positive is worse than a miss: it stops a company from being scraped and cached."""

    def test_an_ordinary_job_board_is_not_a_block(self) -> None:
        page = ("Software Engineer - Remote - United States. "
                "We are looking for an engineer to join our platform team. "
                "Requirements: 5+ years of experience, Go, Kubernetes.")
        self.assertFalse(classify(status=200, title="Careers at Acme", text=page).blocked)

    def test_an_empty_board_is_not_a_block(self) -> None:
        got = classify(status=200, title="Job Search", text="0 results found. Try a different search.")
        self.assertFalse(got.blocked)
        self.assertEqual(got.reason, "")

    def test_a_posting_that_merely_mentions_security_is_not_a_block(self) -> None:
        # The word "security" appears constantly in engineering job ads; only the real markers count.
        page = ("Security Engineer, Cloud Security, Application Security. "
                "You will work on authentication, access control and human verification systems.")
        self.assertFalse(classify(status=200, text=page).blocked)

    def test_scan_is_bounded(self) -> None:
        # A marker beyond the scan limit is not found: the cost of scanning whole SPA payloads is not
        # worth the rare match.
        page = "ordinary content " * 5000 + " access denied"
        self.assertFalse(classify(status=200, text=page).blocked)


if __name__ == "__main__":
    unittest.main(verbosity=2)
