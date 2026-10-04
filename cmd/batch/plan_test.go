// plan_test.go: which companies a sweep visits, and in what order the options apply.
//
// These exist because the ordering bug they pin down shipped, and nothing covered the filters: -limit
// was applied to the candidate list *before* the skip rules, so asking for five companies while
// skipping already-traced ones could scrape none at all - and report a successful zero. The
// combinations are what matter here, not the individual filters.

package main

import (
	"strings"
	"testing"

	"jobsapp/internal/store"
)

func targets(slugs ...string) []store.ScrapeTarget {
	out := make([]store.ScrapeTarget, 0, len(slugs))
	for _, slug := range slugs {
		out = append(out, store.ScrapeTarget{Slug: slug})
	}
	return out
}

func plannedSlugs(planned []store.ScrapeTarget) string {
	slugs := make([]string, 0, len(planned))
	for _, target := range planned {
		slugs = append(slugs, target.Slug)
	}
	return strings.Join(slugs, ",")
}

// skipSaying mimics shouldSkip: a company is skipped when it is in the "done" set.
func skipSaying(done ...string) func(store.ScrapeTarget) string {
	set := map[string]bool{}
	for _, slug := range done {
		set[slug] = true
	}
	return func(target store.ScrapeTarget) string {
		if set[target.Slug] {
			return "already done"
		}
		return ""
	}
}

func TestPlanSweepLimitAppliesAfterTheSkipRules(t *testing.T) {
	// The regression. Five candidates are already done; a request for five companies must still find
	// five to do, not spend its whole quota on the ones it is about to skip.
	candidates := targets("a", "b", "c", "d", "e", "f", "g")

	planned, skipped, considered := planSweep(
		candidates, nil, "", 5, skipSaying("a", "b", "c", "d", "e"))

	if got := plannedSlugs(planned); got != "f,g" {
		t.Errorf("planned = %q, want f,g", got)
	}
	if len(skipped) != 5 {
		t.Errorf("skipped = %d, want 5", len(skipped))
	}
	if considered != 7 {
		t.Errorf("considered = %d, want 7", considered)
	}
}

func TestPlanSweepNothingLeftAfterSkippingIsAnHonestEmpty(t *testing.T) {
	// Every candidate skipped is not an error, but it must be distinguishable from "no candidates" -
	// otherwise the sweep looks like a successful run that did nothing.
	planned, skipped, _ := planSweep(targets("a", "b"), nil, "", 5, skipSaying("a", "b"))

	if len(planned) != 0 {
		t.Errorf("planned = %v, want nothing", planned)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %d, want 2", len(skipped))
	}
	if skipped[0].Reason == "" {
		t.Error("a skip must carry its reason")
	}
	if skipped[1].Index != 1 {
		t.Errorf("skipped[1].Index = %d, want 1 (its position among the candidates)", skipped[1].Index)
	}
}

func TestPlanSweepOnlySlugsBeatsTheCap(t *testing.T) {
	// A slug late in the alphabet must still be reachable. Applying the cap first would take the first
	// five candidates and then filter, dropping the one that was asked for while the quota is spent.
	candidates := targets("a", "b", "c", "d", "e", "f", "zebra")

	planned, _, considered := planSweep(candidates, []string{"zebra"}, "", 1, skipSaying())

	if got := plannedSlugs(planned); got != "zebra" {
		t.Errorf("planned = %q, want zebra", got)
	}
	if considered != 1 {
		t.Errorf("considered = %d, want 1 after the slug filter", considered)
	}
}

func TestPlanSweepOnlySlugsAndCapTogether(t *testing.T) {
	candidates := targets("a", "b", "c", "d")

	planned, _, considered := planSweep(candidates, []string{"b", "c", "d"}, "", 2, skipSaying())

	if got := plannedSlugs(planned); got != "b,c" {
		t.Errorf("planned = %q, want b,c (the cap applies to the selection, in candidate order)", got)
	}
	if considered != 3 {
		t.Errorf("considered = %d, want 3", considered)
	}
}

func TestPlanSweepFromSlugThenCap(t *testing.T) {
	candidates := targets("a", "b", "c", "d", "e")

	planned, _, considered := planSweep(candidates, nil, "c", 2, skipSaying())

	if got := plannedSlugs(planned); got != "c,d" {
		t.Errorf("planned = %q, want c,d", got)
	}
	if considered != 3 {
		t.Errorf("considered = %d, want 3 (c, d, e)", considered)
	}
}

func TestPlanSweepFromSlugWithSkipsStillFillsTheQuota(t *testing.T) {
	// The three options together: resume point, skip rule, cap.
	candidates := targets("a", "b", "c", "d", "e", "f")

	planned, skipped, _ := planSweep(candidates, nil, "b", 2, skipSaying("b", "c"))

	if got := plannedSlugs(planned); got != "d,e" {
		t.Errorf("planned = %q, want d,e", got)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %d, want 2", len(skipped))
	}
}

func TestPlanSweepWithoutALimitTakesEverythingThatIsLeft(t *testing.T) {
	planned, skipped, _ := planSweep(targets("a", "b", "c"), nil, "", 0, skipSaying("b"))

	if got := plannedSlugs(planned); got != "a,c" {
		t.Errorf("planned = %q, want a,c", got)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped = %d, want 1", len(skipped))
	}
}

func TestPlanSweepStopAfterEnoughIsReached(t *testing.T) {
	// The cap stops the scan early rather than planning everything and truncating, which matters when
	// the skip rule is expensive (it stats a trace file per company).
	checked := 0
	counting := func(target store.ScrapeTarget) string {
		checked++
		return ""
	}
	planned, _, _ := planSweep(targets("a", "b", "c", "d", "e"), nil, "", 2, counting)

	if got := plannedSlugs(planned); got != "a,b" {
		t.Errorf("planned = %q, want a,b", got)
	}
	if checked != 2 {
		t.Errorf("skip rule called %d times, want 2: the scan should stop once the quota is filled", checked)
	}
}

func TestPlanSweepEmptyInputs(t *testing.T) {
	planned, skipped, considered := planSweep(nil, nil, "", 5, skipSaying())
	if len(planned) != 0 || len(skipped) != 0 || considered != 0 {
		t.Errorf("empty input gave planned=%v skipped=%v considered=%d", planned, skipped, considered)
	}

	// A slug list that matches nothing is also an honest empty, not a reason to fall back to everything.
	planned, skipped, considered = planSweep(targets("a", "b"), []string{"nope"}, "", 5, skipSaying())
	if len(planned) != 0 || len(skipped) != 0 || considered != 0 {
		t.Errorf("unmatched slugs gave planned=%v skipped=%v considered=%d", planned, skipped, considered)
	}
}
