// select_test.go: tests for the homepage-selection heuristic.
//
// The measured case is Apple: 109 P856 values, none preferred, mostly regional storefronts. Any
// heuristic that passes that case and the preferred-rank cases is doing the work; rank alone is not,
// because Apple has no preferred value to prefer.
package homepage

import "testing"

func cand(url, rank string) Candidate { return Candidate{URL: url, Rank: rank} }

// appleValues is a representative slice of Apple's real P856 set: one bare origin, one deprecated
// entry, and a crowd of locale storefronts. The bare origin must win.
func appleValues() []Candidate {
	return []Candidate{
		cand("https://apple.com/az/", "normal"),
		cand("https://apple.com/mu/", "normal"),
		cand("https://apple.com/om-ar/", "normal"),
		cand("https://apple.com/la/", "normal"),
		cand("https://apple.com/br/", "normal"),
		cand("https://apple.com/tw", "normal"),
		cand("https://apple.com/uk/", "normal"),
		cand("https://apple.com/", "normal"),
		cand("http://apple.com/stale", "deprecated"),
	}
}

// design: docs/sp1500-plan.md 4.7. Apple is the case that breaks "take the first value" and also
// breaks "prefer the preferred rank", because it has no preferred value at all.
func TestAppleSelectsTheBareOriginDespite109Values(t *testing.T) {
	got := SelectHomepage("Apple Inc.", appleValues())
	if got.URL != "https://apple.com/" {
		t.Fatalf("URL = %q, want the bare origin. Rejected %d candidates", got.URL, got.Rejected)
	}
	if !got.Bare {
		t.Error("Bare = false for a bare origin")
	}
}

// Rank is load-bearing for most entities even though it is a no-op for Apple.
func TestPrefersThePreferredRank(t *testing.T) {
	// Microsoft's shape: a preferred corporate site alongside normal regional values.
	got := SelectHomepage("Microsoft", []Candidate{
		cand("https://www.microsoft.com/en-us/", "normal"),
		cand("https://www.microsoft.com/", "preferred"),
		cand("https://www.microsoft.com/en-gb/", "normal"),
	})
	if got.URL != "https://www.microsoft.com/" {
		t.Errorf("URL = %q, want the preferred value", got.URL)
	}
	if got.Rank != "preferred" {
		t.Errorf("Rank = %q, want preferred", got.Rank)
	}
}

// A deprecated value is a statement that the URL is wrong.
func TestRejectsDeprecatedValues(t *testing.T) {
	got := SelectHomepage("Acme", []Candidate{
		cand("https://acme.example.com/", "deprecated"),
	})
	if got.URL != "" {
		t.Errorf("URL = %q, want empty when the only value is deprecated", got.URL)
	}
	if got.Reason != "no_usable_candidate" {
		t.Errorf("Reason = %q", got.Reason)
	}
}

// Locale-only paths are storefronts, not homepages.
func TestHomepageHeuristicRejectsLocalePaths(t *testing.T) {
	for _, url := range []string{
		"https://example.com/en-us",
		"https://example.com/en-us/",
		"https://example.com/en",
		"https://example.com/pt_BR",
		"https://example.com/fr-ca/",
	} {
		got := SelectHomepage("Example", []Candidate{cand(url, "normal")})
		if got.URL != "" {
			t.Errorf("SelectHomepage(%q) = %q, want it rejected as a locale path", url, got.URL)
		}
	}
}

// A path that names a different kind of page is not the front door, even when it is the only value.
func TestHomepageHeuristicRejectsStoreAndSupportPaths(t *testing.T) {
	for _, url := range []string{
		"https://example.com/investors",
		"https://example.com/investor-relations",
		"https://example.com/newsroom",
		"https://example.com/support",
		"https://example.com/shop",
		"https://example.com/store",
		"https://example.com/careers",
		"https://example.com/contact-us",
	} {
		got := SelectHomepage("Example", []Candidate{cand(url, "normal")})
		if got.URL != "" {
			t.Errorf("SelectHomepage(%q) = %q, want it rejected as a non-homepage path", url, got.URL)
		}
	}
}

// A bare origin beats a deeper path, which is what makes the Apple case work.
func TestPrefersABareOriginOverADeepPath(t *testing.T) {
	got := SelectHomepage("Example", []Candidate{
		cand("https://example.com/company/about-us/our-story", "normal"),
		cand("https://example.com/", "normal"),
	})
	if got.URL != "https://example.com/" {
		t.Errorf("URL = %q, want the bare origin", got.URL)
	}
}

// A host that names the company beats one that does not, which matters when a property points at a
// subsidiary or a brand domain.
func TestPrefersAHostThatNamesTheCompany(t *testing.T) {
	got := SelectHomepage("Acme Corporation", []Candidate{
		cand("https://holdings.example.com/", "normal"),
		cand("https://acme.example.com/", "normal"),
	})
	if got.URL != "https://acme.example.com/" {
		t.Errorf("URL = %q, want the host naming the company", got.URL)
	}
}

// https wins a tie, but not against a better path shape.
func TestPrefersHttpsOnATie(t *testing.T) {
	got := SelectHomepage("Example", []Candidate{
		cand("http://example.com/", "normal"),
		cand("https://example.com/", "normal"),
	})
	if got.URL != "https://example.com/" {
		t.Errorf("URL = %q, want https", got.URL)
	}
}

func TestRejectsNonHTTPAndUnparseableValues(t *testing.T) {
	got := SelectHomepage("Example", []Candidate{
		cand("mailto:hello@example.com", "normal"),
		cand("javascript:void(0)", "normal"),
		cand("not a url at all", "normal"),
		cand("https://", "normal"),
	})
	if got.URL != "" {
		t.Errorf("URL = %q, want empty", got.URL)
	}
	if got.Rejected != 4 {
		t.Errorf("Rejected = %d, want 4", got.Rejected)
	}
}

func TestNoCandidatesReportsWhy(t *testing.T) {
	got := SelectHomepage("Example", nil)
	if got.URL != "" || got.Reason != "no_candidates" {
		t.Errorf("got %+v, want an empty selection with reason no_candidates", got)
	}
}

// An empty company name must not make the token check fire on everything. The heuristic stays
// permissive and the caller decides; what matters is that it does not crash or pick arbitrarily.
func TestEmptyCompanyNameStillSelects(t *testing.T) {
	got := SelectHomepage("", []Candidate{
		cand("https://example.com/az/", "normal"),
		cand("https://example.com/", "normal"),
	})
	if got.URL != "https://example.com/" {
		t.Errorf("URL = %q, want the bare origin even with no company name", got.URL)
	}
}

// Two runs over the same set must agree, or a rerun produces a different website.
func TestSelectionIsDeterministic(t *testing.T) {
	values := appleValues()
	first := SelectHomepage("Apple Inc.", values)
	for i := 0; i < 10; i++ {
		again := SelectHomepage("Apple Inc.", values)
		if again.URL != first.URL {
			t.Fatalf("selection changed between runs: %q then %q", first.URL, again.URL)
		}
	}
}

// Verbose and path-heavy values must not beat a clean origin even in bulk.
func TestManyNoisyValuesStillResolveToTheOrigin(t *testing.T) {
	var values []Candidate
	for _, locale := range []string{"az", "mu", "om-ar", "la", "br", "tw", "uk", "de", "fr", "jp", "kr", "mx"} {
		values = append(values, cand("https://example.com/"+locale+"/", "normal"))
	}
	values = append(values, cand("https://example.com/", "normal"))

	got := SelectHomepage("Example", values)
	if got.URL != "https://example.com/" {
		t.Errorf("URL = %q, want the bare origin out of %d noisy values", got.URL, len(values))
	}
}
