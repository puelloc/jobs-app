package sp1500

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fixture reads one of the captured action=raw payloads. No test in this package touches the
// network: the three pages are checked in byte for byte, so a parse change is measured against a
// fixed input.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func parseFixture(t *testing.T, index Index, name string) Result {
	t.Helper()
	got, err := Parse(index, fixture(t, name))
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return got
}

// names returns the parsed company names in page order.
func names(r Result) []string {
	out := make([]string, 0, len(r.Companies))
	for _, c := range r.Companies {
		out = append(out, c.Name)
	}
	return out
}

// design: docs/sp1500-plan.md 4.2. A data row is a row block carrying a ticker template; the row
// count is the parser's primary correctness signal.
//
// 503 + 399 + 600 = 1502. Derived, not copied: an earlier draft of the plan asserted both 1501
// (before the CBOE row was found) and 1503 (a mis-sum), so the count is asserted per page as well
// as in total, and a wrong total cannot hide behind a right page.
func TestSP500ParserReturns503SourceRows(t *testing.T) {
	got := parseFixture(t, SP500, "sp500.wiki")
	if len(got.Companies) != 503 {
		t.Errorf("parsed %d rows, want 503", len(got.Companies))
	}
}

func TestSP400ParserReturns399SourceRows(t *testing.T) {
	got := parseFixture(t, SP400, "sp400.wiki")
	if len(got.Companies) != 399 {
		t.Errorf("parsed %d rows, want 399", len(got.Companies))
	}
}

func TestSP600ParserReturns600SourceRows(t *testing.T) {
	got := parseFixture(t, SP600, "sp600.wiki")
	if len(got.Companies) != 600 {
		t.Errorf("parsed %d rows, want 600", len(got.Companies))
	}
}

func TestSP1500ParserReturns1502SourceRows(t *testing.T) {
	total := 0
	for _, tc := range []struct {
		index Index
		file  string
	}{
		{SP500, "sp500.wiki"},
		{SP400, "sp400.wiki"},
		{SP600, "sp600.wiki"},
	} {
		total += len(parseFixture(t, tc.index, tc.file).Companies)
	}
	if total != 1502 { // 503 + 399 + 600
		t.Errorf("parsed %d rows across the three pages, want 1502", total)
	}
}

// design: docs/sp1500-plan.md 4.4. Ticker templates also appear in prose inside the change-log
// tables further down each page. Counting those inflates the result - it produced a bogus 601 for
// the S&P 600 - and it fails by returning a plausible wrong number rather than an error.
//
// STEL is the decisive case: it appears ONLY in change-log prose and is not a constituent, whereas
// ADEA and PRG appear in both places and are. So the assertion is "600 rows, and STEL is not among
// the names", not "STEL is present".
func TestParserIgnoresTickerTemplatesOutsideComponentTable(t *testing.T) {
	page := fixture(t, "sp600.wiki")
	if !strings.Contains(page, "{{NASDAQ|STEL}}") {
		t.Fatal("fixture no longer contains the change-log prose this test guards; it has been re-captured and the guard is untested")
	}

	got := parseFixture(t, SP600, "sp600.wiki")
	if len(got.Companies) != 600 {
		t.Errorf("parsed %d rows, want 600: prose ticker mentions outside the table were counted", len(got.Companies))
	}
	// The last constituent row is Zurn Elkay Water Solutions Corp. (ZWS). If the range ran past
	// the component table, that name would be joined by change-log entries.
	if got.Companies[len(got.Companies)-1].Name != "Zurn Elkay Water Solutions Corp." {
		t.Errorf("last parsed row = %q, want %q", got.Companies[len(got.Companies)-1].Name, "Zurn Elkay Water Solutions Corp.")
	}
}

// design: docs/sp1500-plan.md 4.3. {{BZX link|CBOE}} carries the 503rd S&P 500 row. Dropping the
// template loses the row entirely.
func TestParserHandlesBzxLinkTemplate(t *testing.T) {
	got := parseFixture(t, SP500, "sp500.wiki")
	for _, c := range got.Companies {
		if c.Name == "Cboe Global Markets" || strings.HasPrefix(c.Name, "Cboe") {
			return
		}
	}
	t.Errorf("CBOE is missing; {{BZX link}} is not being matched. Names near the start of the sorted order: %v", names(got)[:5])
}

// design: docs/sp1500-plan.md 4.3. The S&P 600 glues an anchor to the ticker: |{{Anchor|A}}{{NyseSymbol|AAMI}}.
func TestParserHandlesBarePipeCellSyntax(t *testing.T) {
	got := parseFixture(t, SP600, "sp600.wiki")
	for _, c := range got.Companies {
		if c.Article != "Acadian Asset Management" {
			continue
		}
		if c.Name != "Acadian Asset Management Inc." {
			t.Errorf("AAMI name = %q, want %q", c.Name, "Acadian Asset Management Inc.")
		}
		if c.Industry != "Financials" {
			t.Errorf("AAMI industry = %q, want %q", c.Industry, "Financials")
		}
		return
	}
	t.Error("the Acadian Asset Management row is missing from the S&P 600 parse")
}

// design: docs/sp1500-plan.md 4.3. The S&P 400 uses | style="border-color:inherit;" | cells, so a
// naive split leaks the style attribute into the name.
func TestParserHandlesStyledCellSyntax(t *testing.T) {
	got := parseFixture(t, SP400, "sp400.wiki")
	if len(got.Companies) == 0 {
		t.Fatal("no rows parsed")
	}
	for _, c := range got.Companies {
		if strings.Contains(c.Name, "border-color") || strings.Contains(c.Name, "style=") {
			t.Fatalf("row %q kept the cell style attribute in its name", c.Name)
		}
	}
}

// design: docs/sp1500-plan.md 4.5. 43 S&P 400 rows have a plain-text Security cell because the
// company has no Wikipedia article. That is a legitimate outcome, not a parse failure, and it is
// why article title is only a ~83% join key.
func TestSP400ParserHandlesPlainTextCompanyNames(t *testing.T) {
	got := parseFixture(t, SP400, "sp400.wiki")

	found := map[string]Company{}
	for _, c := range got.Companies {
		found[c.Name] = c
	}
	for _, name := range []string{
		"Darling Ingredients",
		"EastGroup Properties",
		"Kinsale Capital Group",
	} {
		c, ok := found[name]
		if !ok {
			t.Errorf("%q is missing from the S&P 400 rows", name)
			continue
		}
		if c.HasArticle {
			t.Errorf("%q reported a wikilink article %q, but its cell is plain text", name, c.Article)
		}
		if c.Industry == "" {
			t.Errorf("%q parsed with no industry", name)
		}
	}
}

func TestPlainTextSecurityCellsAreCountedPerPage(t *testing.T) {
	for _, tc := range []struct {
		index Index
		file  string
		want  int
	}{
		// Measured: 1 / 43 / 208. The S&P 500's single plain-text row is Insulet. An earlier
		// figure of 209 for the S&P 600 came from a hand probe and was off by one; this is the
		// count the parser produces and an independent re-derivation agrees.
		{SP500, "sp500.wiki", 1},
		{SP400, "sp400.wiki", 43},
		{SP600, "sp600.wiki", 208},
	} {
		got := parseFixture(t, tc.index, tc.file)
		plain := 0
		for _, c := range got.Companies {
			if !c.HasArticle {
				plain++
			}
		}
		if plain != tc.want {
			t.Errorf("%s: %d plain-text rows, want %d", tc.index, plain, tc.want)
		}
	}
}

// design: docs/sp1500-plan.md 4.3. All six template spellings are in use across the three pages and
// each one carries a row.
func TestExtractsRowsFromAllSixTemplateVariants(t *testing.T) {
	synthetic := `{| class="wikitable"
|-
! Symbol
! Security
|-
|| {{NyseSymbol|AAA}}
|| [[Alpha]]
|-
|| {{NasdaqSymbol|BBB}}
|| [[Beta]]
|-
|| {{BZX link|CCC}}
|| [[Gamma]]
|-
|| {{NYSE Arca|DDD}}
|| [[Delta]]
|-
|| {{nyse|EEE}}
|| [[Epsilon]]
|-
|| {{NASDAQ|FFF}}
|| [[Zeta]]
|}`
	got, err := Parse(SP500, synthetic)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta"}
	if strings.Join(names(got), ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v", names(got), want)
	}
}

// design: docs/sp1500-plan.md 4.6. The S&P 500 and S&P 600 have a numeric CIK column; the S&P 400
// does not. CIK is the join key for anything outside Wikipedia, so it is worth getting right.
func TestExtractsCikFromBareNumericCellForSP500(t *testing.T) {
	got := parseFixture(t, SP500, "sp500.wiki")
	byName := map[string]Company{}
	for _, c := range got.Companies {
		byName[c.Name] = c
	}
	for name, want := range map[string]int64{
		"3M":         66740,  // 0000066740
		"Apple Inc.": 320193, // 0000320193
	} {
		c, ok := byName[name]
		if !ok {
			t.Errorf("%q is missing", name)
			continue
		}
		if c.CIK != want {
			t.Errorf("%s CIK = %d, want %d (raw %q)", name, c.CIK, want, c.RawCIK)
		}
	}
}

// design: docs/sp1500-plan.md 4.6. The S&P 400 CIK= value is ticker-like for most rows. It is kept
// verbatim rather than coerced: resolving it needs SEC company_tickers.json, and a half-resolved
// integer would be worse than none.
func TestSP400StoresTickerLikeCikVerbatimWithoutCoercion(t *testing.T) {
	got := parseFixture(t, SP400, "sp400.wiki")

	byName := map[string]Company{}
	for _, c := range got.Companies {
		byName[c.Name] = c
	}
	darling, ok := byName["Darling Ingredients"]
	if !ok {
		t.Fatal("Darling Ingredients is missing from the S&P 400 rows")
	}
	if darling.RawCIK != "DAR" {
		t.Errorf("Darling Ingredients RawCIK = %q, want %q", darling.RawCIK, "DAR")
	}
	if darling.CIK != 0 {
		t.Errorf("Darling Ingredients CIK = %d, want 0 for a ticker-like CIK", darling.CIK)
	}

	// Every S&P 400 row must yield a CIK= value, whether numeric or ticker-like.
	missing := 0
	for _, c := range got.Companies {
		if c.RawCIK == "" {
			missing++
		}
	}
	if missing != 0 {
		t.Errorf("%d S&P 400 rows have no CIK= at all, want 0", missing)
	}
}

// design: docs/sp1500-plan.md 4.6. The S&P 600's CIK comes from its own column, not from the SEC
// link's CIK= parameter.
func TestExtractsCikFromCikColumnForSP600(t *testing.T) {
	got := parseFixture(t, SP600, "sp600.wiki")
	for _, c := range got.Companies {
		if c.Article == "Acadian Asset Management" {
			if c.CIK != 1748824 {
				t.Errorf("Acadian CIK = %d, want 1748824 (raw %q)", c.CIK, c.RawCIK)
			}
			return
		}
	}
	t.Fatal("the Acadian Asset Management row is missing")
}

func TestParsedRowsCarryIndustryAndHeadquarters(t *testing.T) {
	got := parseFixture(t, SP500, "sp500.wiki")
	byName := map[string]Company{}
	for _, c := range got.Companies {
		byName[c.Name] = c
	}
	aapl, ok := byName["Apple Inc."]
	if !ok {
		t.Fatal("Apple Inc. is missing")
	}
	if aapl.Article != "Apple Inc." {
		t.Errorf("Apple article = %q, want %q", aapl.Article, "Apple Inc.")
	}
	if aapl.Industry != "Information Technology" {
		t.Errorf("Apple industry = %q, want %q", aapl.Industry, "Information Technology")
	}
	if aapl.SubIndustry != "Technology Hardware, Storage & Peripherals" {
		t.Errorf("Apple sub-industry = %q", aapl.SubIndustry)
	}
	if aapl.Headquarters != "Cupertino, California" {
		t.Errorf("Apple headquarters = %q, want %q", aapl.Headquarters, "Cupertino, California")
	}
}

// Industry and Headquarters are the columns the jobs work actually consumes, so a parse that
// silently leaves them empty for a whole page would be worse than a wrong row count.
func TestEveryRowHasNameAndIndustry(t *testing.T) {
	for _, tc := range []struct {
		index Index
		file  string
	}{
		{SP500, "sp500.wiki"},
		{SP400, "sp400.wiki"},
		{SP600, "sp600.wiki"},
	} {
		got := parseFixture(t, tc.index, tc.file)
		for i, c := range got.Companies {
			if strings.TrimSpace(c.Name) == "" {
				t.Fatalf("%s row %d has an empty name", tc.index, i)
			}
			if strings.TrimSpace(c.Industry) == "" {
				t.Fatalf("%s row %d (%s) has an empty industry", tc.index, i, c.Name)
			}
		}
	}
}

// Dual-class listings (GOOGL/GOOG, FOXA/FOX, NWSA/NWS, UA/UAA) are separate rows for one company.
// The parser returns them as separate records on purpose; collapsing them is the slug's job.
func TestDualClassRowsCollapseToOneName(t *testing.T) {
	got := parseFixture(t, SP500, "sp500.wiki")
	count := 0
	for _, c := range got.Companies {
		if strings.HasPrefix(c.Name, "Alphabet Inc.") {
			count++
		}
	}
	if count != 2 {
		t.Errorf("Alphabet appears %d times, want 2 (GOOGL and GOOG share one article)", count)
	}
}

func TestCleanTextStripsWikiMarkup(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"[[Milwaukee]], Wisconsin", "Milwaukee, Wisconsin"},
		{"[[3M]]", "3M"},
		{"[[Burlington (department store)|Burlington Stores]]", "Burlington Stores"},
		{"Plain Name", "Plain Name"},
		{"[[AT&T]]", "AT&T"},
		{"Name<ref>{{cite web|url=x}}</ref>", "Name"},
		{"Name<!-- comment -->", "Name"},
		// Consecutive capture-group references are mishandled by Go's regexp, so the piped
		// form is the regression that matters most here.
		{"[[A. O. Smith|A. O. Smith Corporation]]", "A. O. Smith Corporation"},
	} {
		if got := cleanText(tc.in); got != tc.want {
			t.Errorf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestComponentTableErrorsOnAnUnclosedTable(t *testing.T) {
	_, err := Parse(SP500, "{| class=\"wikitable\"\n|-\n|| {{NyseSymbol|AAA}}\n")
	if err == nil {
		t.Fatal("Parse accepted an unclosed table, want an error")
	}
}

func TestComponentTableErrorsWhenThereIsNoTable(t *testing.T) {
	_, err := Parse(SP500, "Just some prose with no table at all.\n")
	if err == nil {
		t.Fatal("Parse accepted a page with no table, want an error")
	}
}

// design: docs/sp1500-plan.md 6.5 rule 1. A nested table inside a cell must not truncate the range,
// because a truncated range yields a smaller row count that looks like a legitimate page change.
func TestParserHandlesNestedTableInCell(t *testing.T) {
	synthetic := `{| class="wikitable"
|-
! Symbol
! Security
|-
|| {{NyseSymbol|AAA}}
|| [[Alpha]]
|-
|| {{NyseSymbol|BBB}}
|| [[Beta]]
{| class="nested"
|-
|| nested
|}
|-
|| {{NyseSymbol|CCC}}
|| [[Gamma]]
|}`
	got, err := Parse(SP500, synthetic)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Companies) != 3 {
		t.Errorf("parsed %d rows, want 3: a nested table truncated the component range", len(got.Companies))
	}
}

// The captured pages contain no nested tables, which is what makes depth counting equivalent to
// first-close today rather than merely convenient.
func TestFixturesHaveNoNestedTables(t *testing.T) {
	for _, name := range []string{"sp500.wiki", "sp400.wiki", "sp600.wiki"} {
		lines := strings.Split(fixture(t, name), "\n")
		start := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "{|") {
				start = i
				break
			}
		}
		if start == -1 {
			t.Fatalf("%s: no table", name)
		}
		depth, maxDepth := 0, 0
		for i := start; i < len(lines); i++ {
			depth += strings.Count(lines[i], "{|")
			if depth > maxDepth {
				maxDepth = depth
			}
			depth -= strings.Count(lines[i], "|}")
		}
		if maxDepth != 1 {
			t.Errorf("%s reaches table depth %d, want 1; the fixtures gained a nested table and the range logic now matters", name, maxDepth)
		}
	}
}

// A dual-class listing puts one company on two rows sharing a Security name, so the upsert collapses
// them into one companies row. This pins the exact set, by name, because a count alone cannot show
// which pair changed: the point is to notice a *fifth* pair appearing when the index changes, and a
// pair silently ceasing to collapse.
//
// Expected four, from the three pages:
//
//	Fox Corporation   (FOXA, FOX)      S&P 500
//	News Corp         (NWSA, NWS)      S&P 500
//	Alphabet Inc.     (GOOGL, GOOG)    S&P 500
//	Under Armour      (UA, UAA)        S&P 600
//
// The obvious fifth candidate is absent: Berkshire Hathaway and Brown-Forman each appear on a
// single row in the current index, so the 1,502 rows collapse to 1,498 companies and not fewer.
func TestSP1500UpsertCollapsesExactlyFourDualClassPairs(t *testing.T) {
	groups := map[string][]Company{}
	for _, tc := range []struct {
		index Index
		file  string
	}{
		{SP500, "sp500.wiki"},
		{SP400, "sp400.wiki"},
		{SP600, "sp600.wiki"},
	} {
		for key, rows := range shareClassGroups(parseFixture(t, tc.index, tc.file)) {
			if len(rows) > 1 {
				groups[key] = rows
			}
		}
	}

	got := make([]string, 0, len(groups))
	for _, rows := range groups {
		got = append(got, rows[0].Name)
	}
	sort.Strings(got)

	// Name AND index. The count catches a fifth pair appearing; the names catch a swap; the indices
	// catch a pair crossing between pages, which would silently change which page owns the row.
	want := []struct {
		name  string
		index Index
	}{
		{"Alphabet Inc.", SP500},
		{"Fox Corporation", SP500},
		{"News Corp", SP500},
		{"Under Armour", SP600},
	}
	if len(got) != len(want) {
		t.Fatalf("dual-class groups = %v, want %d", got, len(want))
	}
	for i, w := range want {
		if got[i] != w.name {
			t.Errorf("group %d = %q, want %q", i, got[i], w.name)
		}
	}

	// Index is tracked per page so a pair that migrates between indices is caught.
	for _, tc := range []struct {
		index Index
		file  string
		names []string
	}{
		{SP500, "sp500.wiki", []string{"Alphabet Inc.", "Fox Corporation", "News Corp"}},
		{SP400, "sp400.wiki", nil},
		{SP600, "sp600.wiki", []string{"Under Armour"}},
	} {
		var names []string
		for _, rows := range shareClassGroups(parseFixture(t, tc.index, tc.file)) {
			if len(rows) > 1 {
				names = append(names, rows[0].Name)
			}
		}
		sort.Strings(names)
		if strings.Join(names, "|") != strings.Join(tc.names, "|") {
			t.Errorf("%s dual-class names = %v, want %v", tc.index, names, tc.names)
		}
	}

	// Each group is exactly one pair, and both rows carry the same name.
	for key, rows := range groups {
		if len(rows) != 2 {
			t.Errorf("group %q has %d rows, want 2", key, len(rows))
		}
		for _, c := range rows {
			if c.Name != rows[0].Name {
				t.Errorf("group %q mixes names %q and %q", key, rows[0].Name, c.Name)
			}
		}
	}

	// Row total minus collapapses must equal the company count the upsert produces.
	rows, companies := 0, 0
	for _, tc := range []struct {
		index Index
		file  string
	}{
		{SP500, "sp500.wiki"},
		{SP400, "sp400.wiki"},
		{SP600, "sp600.wiki"},
	} {
		parsed := parseFixture(t, tc.index, tc.file)
		rows += len(parsed.Companies)
		companies += len(shareClassGroups(parsed))
	}
	if rows != 1502 {
		t.Errorf("parsed rows = %d, want 1502", rows)
	}
	if companies != 1498 {
		t.Errorf("distinct companies = %d, want 1498 (%d rows less %d collapses)", companies, rows, len(groups))
	}
}

// The names that look like dual-class candidates but are not in the current index. Pinned because
// the assumption "Berkshire must be two rows" is natural and wrong here.
func TestBerkshireAndBrownFormanAreSingleRows(t *testing.T) {
	got := parseFixture(t, SP500, "sp500.wiki")
	for _, want := range []string{"Berkshire Hathaway", "Brown–Forman"} {
		n := 0
		for _, c := range got.Companies {
			if c.Name == want {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%q appears on %d rows, want 1: if the index gained a second share class, the collapse count changes", want, n)
		}
	}
}

// collapseKey duplicates the companies.slug normalisation, so its edge cases must be exercised on
// both sides or the two can drift while each table still passes. This mirrors the store's
// TestIndexCompanySlugCollapsesCosmeticVariation input for input, plus the punctuation that actually
// occurs on these pages: "Brown–Forman" uses an EN DASH, not a hyphen.
func TestCollapseKeyMatchesTheStoreSlugOnSharedEdgeCases(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Apple Inc.", "apple-inc"},
		{"Alphabet Inc.", "alphabet-inc"},
		{"3M", "3m"},
		{"AT&T", "at-t"},
		{"A. O. Smith", "a-o-smith"},
		{"  Spaced   Out  ", "spaced-out"},
		// Real page data. The en dash and the ampersand both collapse to one hyphen.
		{"Brown–Forman", "brown-forman"},
		{"Fox Corporation", "fox-corporation"},
		{"News Corp", "news-corp"},
		{"Under Armour", "under-armour"},
		{"Johnson & Johnson", "johnson-johnson"},
	} {
		if got := collapseKey(tc.in); got != tc.want {
			t.Errorf("collapseKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A hyphen and an en dash are different bytes and the same key, which is the property that lets
// Brown–Forman be matched at all.
func TestCollapseKeyTreatsHyphenAndEnDashAlike(t *testing.T) {
	if collapseKey("Brown-Forman") != collapseKey("Brown–Forman") {
		t.Errorf("hyphen and en dash produced different keys: %q vs %q",
			collapseKey("Brown-Forman"), collapseKey("Brown–Forman"))
	}
}

// The S&P 400 has no dual-class listing today. Pinned separately so that when one appears it fails
// here, naming the page, rather than only moving a total in another test.
func TestSP400HasNoDualClassCollapse(t *testing.T) {
	for key, rows := range shareClassGroups(parseFixture(t, SP400, "sp400.wiki")) {
		if len(rows) > 1 {
			t.Errorf("the S&P 400 gained a dual-class listing: key %q covers %d rows (%q)", key, len(rows), rows[0].Name)
		}
	}
}
