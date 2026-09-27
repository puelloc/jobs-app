// vendor_test.go: tests for the vendor fingerprint against real-world URL and DOM shapes.
package careers

import "testing"

func TestFingerprint_ByURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://boards.greenhouse.io/acme", "greenhouse"},
		{"https://jobs.lever.co/acme", "lever"},
		{"https://jobs.ashbyhq.com/acme", "ashby"},
		{"https://acme.wd1.myworkdayjobs.com/en-US/acme", "workday"},
		{"https://careers.acme.icims.com/jobs/search", "icims"},
		{"https://acme.taleo.net/careersection/x", "taleo"},
		{"https://acme.oraclecloud.com/hcmUI/CandidateExperience/", "oraclecloud"},
		{"https://acme.sapsf.com/sfcareer", "successfactors"},
		{"https://careers.acme.com/global/en", ""}, // first-party, no host signal
	}
	for _, c := range cases {
		if got := Fingerprint(c.url, ""); got != c.want {
			t.Errorf("Fingerprint(%q, \"\") = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestFingerprint_ByHTML(t *testing.T) {
	const firstParty = "https://careers.acme.com/careers"
	cases := []struct {
		html string
		want string
	}{
		{`<input data-testid="position-query-search-search">`, "eightfold"},
		{`<button data-ph-at-id="globalsearch-button">Search</button>`, "phenom"},
		{`<div>Powered by SuccessFactors</div>`, "successfactors"},
		{`<a href="https://boards.greenhouse.io/acme">Careers</a>`, "greenhouse"},
		{`<a href="https://acme.wd1.myworkdayjobs.com/acme">Jobs</a>`, "workday"},
		{`<div>nothing to see here</div>`, ""},
	}
	for _, c := range cases {
		if got := Fingerprint(firstParty, c.html); got != c.want {
			t.Errorf("Fingerprint(firstParty, %q) = %q, want %q", c.html, got, c.want)
		}
	}
}

func TestNextHopURL(t *testing.T) {
	const base = "https://www.acme.com/en-us/careers"
	cases := []struct {
		html string
		want string
	}{
		{`<a href="https://jobs.acme.com/careers">See all open positions</a>`, "https://jobs.acme.com/careers"},
		{`<a href="/jobs">Jobs</a>`, "https://www.acme.com/jobs"},
		{`<a href="/about">About us</a>`, ""},
		{`<a href="/en-us/careers">Careers</a>`, ""}, // the current page
	}
	for _, c := range cases {
		if got := NextHopURL(base, c.html); got != c.want {
			t.Errorf("NextHopURL(%q, %q) = %q, want %q", base, c.html, got, c.want)
		}
	}
}

func TestFingerprint_HostWinsOverHTML(t *testing.T) {
	// A redirect to a vendor host beats any DOM signature on the page body.
	got := Fingerprint("https://acme.icims.com/jobs", `<input data-testid="position-query-search-search">`)
	if got != "icims" {
		t.Errorf("Fingerprint = %q, want icims (host beats DOM)", got)
	}
}
