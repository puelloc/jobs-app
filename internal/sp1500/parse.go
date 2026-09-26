// Package sp1500 parses the S&P index constituent tables out of Wikipedia wikitext.
//
// It is a pure function over bytes: it performs no I/O and knows nothing about HTTP, the database,
// or the run lifecycle. The caller fetches the page, persists the raw wikitext, and hands the bytes
// here, so a parse bug is always a re-parse of a stored payload rather than a re-fetch.
//
// Scope: company identity. The three pages are an index of large public companies, and this package
// extracts who they are, not whether they hire. Fields the pages carry but this package discards -
// notably the ticker symbol - are not needed to find a company's careers site.
//
// design: docs/sp1500-plan.md, sections 4.2-4.6.
package sp1500

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Index identifies which constituent list a page holds.
type Index string

const (
	SP500 Index = "sp500"
	SP400 Index = "sp400"
	SP600 Index = "sp600"
)

// Company is one constituent row.
//
// One row yields exactly one Company, including the dual-class rows (GOOGL and GOOG are two rows
// and so two Companies). Collapsing those into one record is a dedupe concern that belongs with the
// slug, not here.
type Company struct {
	// Name is the security name with wiki markup stripped, e.g. "Apple Inc.".
	Name string
	// Article is the target of the Security cell's wikilink, or "" when the cell was plain
	// text. Roughly 253 rows across the three pages have no article at all, which makes article
	// title a ~83% join key rather than a near-complete one. Those rows need a non-Wikipedia
	// identity source, which is why this is recorded rather than assumed.
	Article string
	// HasArticle reports whether the Security cell carried a wikilink.
	HasArticle bool
	// Industry is the GICS Sector as published, e.g. "Information Technology".
	Industry string
	// SubIndustry is the GICS Sub-Industry as published.
	SubIndustry string
	// Headquarters as published, which mixes city/state and country depending on the row.
	Headquarters string
	// CIK is the SEC Central Index Key when the page states a numeric one, else 0.
	//
	// The S&P 500 and S&P 600 pages have an explicit numeric CIK column. The S&P 400 page has
	// no CIK column: its SEC link carries CIK=<value>, and 281 of its 399 values are ticker-like
	// rather than numeric (e.g. CIK=DAR). Those are deliberately not resolved here - doing so
	// needs SEC company_tickers.json, which is a later step - but the raw value is kept in
	// RawCIK so nothing is lost.
	CIK int64
	// RawCIK is the CIK value exactly as it appeared, numeric or ticker-like.
	RawCIK string
}

// Result is the outcome of parsing one page.
type Result struct {
	Index     Index
	Companies []Company
}

// tickerTemplate matches a ticker template anywhere in a row, which is what distinguishes a data row
// from the header or a spacer row. The six spellings below are all in use across the three pages;
// {{BZX link|CBOE}} carries the 503rd S&P 500 row, so omitting it is a silent off-by-one.
var tickerTemplate = regexp.MustCompile(`\{\{(?:NyseSymbol|NasdaqSymbol|BZX link|NYSE Arca|nyse|NASDAQ)\|([A-Za-z0-9.\-]+)\}\}`)

// articleLink matches [[Target|Display]] and [[Target]].
var articleLink = regexp.MustCompile(`\[\[([^\]|#]+)(?:\|([^\]]+))?\]\]`)

// bareNumericCell matches a table cell whose entire content is a run of digits.
var bareNumericCell = regexp.MustCompile(`^\|?\s*(\d{6,10})\s*$`)

// cikParameter matches the CIK= parameter inside an SEC EDGAR link. The leading (?:\?|&(?:amp;)?)
// is required because the raw wikitext writes the second and later query parameters as "&amp;CIK=",
// so anchoring on a bare "&" would miss most S&P 400 rows.
var cikParameter = regexp.MustCompile(`(?:\?|&(?:amp;)?)CIK=([A-Za-z0-9.\-]+)`)

// Parse extracts the constituent table from one index page's wikitext.
//
// index labels the result and is echoed back; it is not inferred from the page.
func Parse(index Index, wikitext string) (Result, error) {
	table, err := componentTable(wikitext)
	if err != nil {
		return Result{Index: index}, err
	}
	rows := splitRows(table)
	if len(rows) == 0 {
		return Result{Index: index}, fmt.Errorf("sp1500: %s: component table has no rows", index)
	}
	header := headerRow(rows)
	hasCIKColumn := headerHasCIKColumn(header)
	headquartersColumn := dataColumn(header, "Headquarters Location")

	var companies []Company
	for _, row := range rows {
		body := strings.Join(row, "\n")
		// Strip HTML comments before slicing at the template. The S&P 500 guards two symbols with
		// "<!-- DO NOT CHANGE THIS TICKER TO BRK-B. -->", and leaving the comment in place makes
		// the slice begin inside it, which corrupts the Security cell.
		body = htmlComment.ReplaceAllString(body, "")
		match := tickerTemplate.FindStringIndex(body)
		if match == nil {
			// The table opener, the header, or a spacer row. Every data row carries a ticker
			// template.
			continue
		}
		// Slicing past the template drops it, so rowCells[0] is the Security cell whether the
		// template ends its cell (S&P 500/400) or shares it with an {{Anchor}} prefix (S&P 600).
		rowCells := cells(body[match[1]:])
		if len(rowCells) == 0 {
			continue
		}

		c := Company{}

		nameCell := rowCells[0]
		if link := articleLink.FindStringSubmatch(nameCell); link != nil {
			c.Article = strings.TrimSpace(link[1])
			c.HasArticle = true
			if link[2] != "" {
				c.Name = normalizeSecurityName(cleanText(link[2]))
			} else {
				c.Name = normalizeSecurityName(cleanText(link[1]))
			}
		} else {
			c.Name = normalizeSecurityName(cleanText(nameCell))
		}

		// Columns are located by header name rather than by fixed position. The pages disagree
		// on layout (the S&P 400 has an extra leading style cell and no Date added or CIK
		// column), and a positional read silently returns a neighbouring column instead of
		// failing. Front-relative reads are safe here because cells() preserves interior empty
		// cells, so a blank Headquarters does not shift the columns after it.
		if i := dataColumn(header, "GICS Sector"); i >= 0 && i < len(rowCells) {
			c.Industry = cleanText(rowCells[i])
		}
		if i := dataColumn(header, "GICS Sub-Industry"); i >= 0 && i < len(rowCells) {
			c.SubIndustry = cleanText(rowCells[i])
		}
		if headquartersColumn >= 0 && headquartersColumn < len(rowCells) {
			c.Headquarters = cleanText(rowCells[headquartersColumn])
		}

		switch {
		case hasCIKColumn:
			// The CIK column is the rightmost column on both pages that have one, and holds
			// pure digits. Scanning from the right skips the Founded year, which is only four
			// digits and so cannot match anyway.
			for i := len(rowCells) - 1; i >= 0; i-- {
				if bareNumericCell.MatchString(strings.TrimSpace(rowCells[i])) {
					c.RawCIK = strings.TrimSpace(rowCells[i])
					break
				}
			}
		default:
			// The S&P 400 page has no CIK column; the value rides in the SEC link.
			if raw := cikParameter.FindStringSubmatch(body); raw != nil {
				c.RawCIK = raw[1]
			}
		}
		if n, err := strconv.ParseInt(strings.TrimLeft(c.RawCIK, "0"), 10, 64); err == nil {
			c.CIK = n
		}

		companies = append(companies, c)
	}

	return Result{Index: index, Companies: companies}, nil
}

// componentTable returns the first table's lines, from the opening "{|" to its depth-matched
// closing "|}".
//
// Depth counting rather than "the first |}" matters: a nested wikitable inside a cell would
// otherwise truncate the range and silently yield a smaller row count, which is indistinguishable
// from a legitimate change to the page.
func componentTable(wikitext string) ([]string, error) {
	lines := strings.Split(wikitext, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "{|") {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, fmt.Errorf("sp1500: no table found")
	}

	depth := 0
	for i := start; i < len(lines); i++ {
		// Count tokens anywhere in the line, not just at column 0: wikitext indents nested
		// tables and their closing braces, and an indented "|}" that went uncounted would
		// leave the range unterminated.
		depth += strings.Count(lines[i], "{|")
		depth -= strings.Count(lines[i], "|}")
		if depth == 0 {
			return lines[start : i+1], nil
		}
		if depth < 0 {
			return nil, fmt.Errorf("sp1500: table opened at line %d closes more often than it opens", start+1)
		}
	}
	return nil, fmt.Errorf("sp1500: table opened at line %d is never closed", start+1)
}

// splitRows groups the table's lines into rows, one per "|-" delimiter.
func splitRows(table []string) [][]string {
	var rows [][]string
	var current []string
	for _, line := range table {
		if strings.HasPrefix(line, "|-") {
			if len(current) > 0 {
				rows = append(rows, current)
			}
			current = nil
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		rows = append(rows, current)
	}
	return rows
}

// cells splits the text following a ticker template into positional cells.
//
// The three pages use two different cell syntaxes and both must be accepted:
//
//	S&P 500  || {{NyseSymbol|MMM}}        each cell on its own line, "||"-prefixed
//	S&P 400  | style="..." | {{NyseSymbol|DAR}}
//	S&P 600  |{{Anchor|A}}{{NyseSymbol|AAMI}}   bare "|" glued to the anchor
//
// A cell boundary is therefore a run of pipes either at the start of a line or doubled. The
// attribute form is markup rather than a cell, and is stripped before splitting: treating its
// surrounding pipe as a boundary would push the Security cell into the second slot, and treating a
// lone space-pipe-space as a boundary anywhere would split wikilinks that contain a spaced pipe,
// such as "[[ABM Industries | ABM Industries, Inc.]]".
func cells(rest string) []string {
	rest = leadingAttribute(rest)

	var out []string
	seenContent := false
	for _, cell := range strings.Split(newlineCellDelimiter.ReplaceAllString(rest, "\x00"), "\x00") {
		trimmed := strings.TrimSpace(cell)
		if trimmed != "" {
			seenContent = true
			out = append(out, trimmed)
			continue
		}
		// Drop empty cells. A leading one appears whenever the Security cell held no wikilink:
		// the cell is then just the "|" that preceded it.
		if seenContent {
			out = append(out, "")
		}
	}
	// Trim trailing empties: the last column genuinely empty carries no information.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// dataColumn returns the index of a named column within the positional cell list built by cells().
//
// The index needs one adjustment: the ticker template and the empty cell that precedes it are both
// dropped before the Security cell, so a header position maps to one earlier slot in the data row.
// Returns -1 when the page has no such column.
func dataColumn(header []string, name string) int {
	for i, cell := range headerCells(header) {
		if cleanText(cell) == name {
			return i - 1
		}
	}
	return -1
}

// headerCells splits the header block into its column labels.
func headerCells(header []string) []string {
	body := strings.Join(header, "\n")
	if openBrace := strings.Index(body, "!"); openBrace != -1 {
		body = body[openBrace+1:]
	}
	// Rewrite header markers so one splitter handles both spellings. The S&P 500 writes
	// "![[Ticker symbol|Symbol]]\n! Security !! GICS Sector !! ...", so the first line ends with no
	// pipe before the newline; normalising newlines to a pipe covers that, and the data-row
	// splitter already tolerates a run of pipes.
	normalised := strings.ReplaceAll(body, "!!", "||")
	normalised = strings.ReplaceAll(normalised, "!", "|")
	normalised = strings.ReplaceAll(normalised, "\n", "|")
	return cells(normalised)
}

// leadingAttribute peels one or more `key="value"` attributes off the front of a cell, used for the
// S&P 400's per-cell "style=..." and for a "|" left stranded by a template.
func leadingAttribute(s string) string {
	for {
		trimmed := strings.TrimLeft(s, " \t\n|")
		m := attribute.FindString(trimmed)
		if m == "" {
			return trimmed
		}
		s = trimmed[len(m):]
	}
}

// headerRow returns the table's header block. It is not rows[0]: splitRows starts a new block only
// at a "|-", so the table opener line "{| class=..." is folded into the first block and the header
// cells follow it.
func headerRow(rows [][]string) []string {
	for _, row := range rows {
		for _, line := range row {
			if strings.HasPrefix(line, "!") {
				return row
			}
		}
	}
	return nil
}

// headerHasCIKColumn reports whether the component table's header declares a CIK column.
//
// The S&P 500 and S&P 600 pages have one; the S&P 400 page does not and carries CIK= in its SEC
// link instead. Only the fact of the column matters: it is the rightmost column wherever it
// appears, so its value is found by scanning from the right.
func headerHasCIKColumn(header []string) bool {
	for _, cell := range headerCells(header) {
		if cleanText(cell) == "CIK" {
			return true
		}
	}
	return false
}

// trailingArticleRE matches the parenthetical article that Wikipedia uses to sort a name under its
// significant word: "Coca-Cola Company (The)", "Hartford (The)".
var trailingArticleRE = regexp.MustCompile(`\s*\((The|A|An)\)\s*$`)

// normalizeSecurityName moves a trailing parenthetical article to the front, so the stored name reads
// the way it is spoken and the slug is usable.
//
// The pages list ten S&P 500 names this way, and without the rewrite "Coca-Cola Company (The)" slugs
// to "coca-cola-company-the", which is neither the company's name nor a useful key. The article title
// for the same row is already "The Coca-Cola Company", so the two agree after this.
func normalizeSecurityName(name string) string {
	m := trailingArticleRE.FindStringSubmatch(name)
	if m == nil {
		return name
	}
	base := strings.TrimSpace(trailingArticleRE.ReplaceAllString(name, ""))
	if base == "" {
		return name
	}
	return m[1] + " " + base
}

// cleanText strips the wiki markup that would otherwise end up stored in a column.
func cleanText(s string) string {
	s = refTag.ReplaceAllString(s, "")
	s = htmlComment.ReplaceAllString(s, "")
	s = template.ReplaceAllString(s, "")
	s = articleLink.ReplaceAllStringFunc(s, linkText)
	s = externalLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "'''", "")
	s = strings.ReplaceAll(s, "''", "")
	return strings.TrimSpace(s)
}

// linkText renders one [[Target|Display]] as its display text, falling back to the target when there
// is no pipe.
//
// This is a callback rather than a "${2}${1}" template because Go's regexp mishandles consecutive
// group references: with both groups present it substitutes them in the wrong positions, so
// "[[Target|Display]]" renders as "DisplayTarget". Verified directly against Go's regexp - "$2"
// alone is correct, "$2$1" is not.
func linkText(link string) string {
	m := articleLink.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	if m[2] != "" {
		return m[2]
	}
	return m[1]
}

var (
	refTag       = regexp.MustCompile(`(?s)<ref[^>]*>.*?</ref>|<ref[^>]*/>`)
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	template     = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	externalLink = regexp.MustCompile(`\[https?://\S+\s+([^\]]+)\]`)
	// A cell boundary, in either spelling: a "||" run, or a pipe run at the start of a line. The
	// run must be consumed whole - a pattern eating only the first pipe of "\n||" leaves a literal
	// "|" at the start of every cell, which then leaks into names, industries, and the CIK lookup.
	newlineCellDelimiter = regexp.MustCompile(`\|\|+|\n[ \t]*\|+`)
	// An inline key="value" cell attribute, i.e. the S&P 400's style="border-color:inherit;".
	attribute = regexp.MustCompile(`^[A-Za-z-]+="[^"]*"`)
)

// shareClassGroups groups rows that share one company name, which is how a dual-class listing
// appears: Alphabet is listed as GOOGL and GOOG on two rows with the same Security name.
//
// The comparison key is the same normalisation the companies.slug uses (lowercase; runs of
// non-alphanumeric characters collapse to a single hyphen; leading and trailing hyphens trimmed),
// duplicated here deliberately. internal/sp1500 does not import the store, and the collapse is a
// property of the parsed data that is worth pinning in the package that produces it. If the two
// ever disagree, this test and the store's slug test fail together, which is the intent.
func shareClassGroups(r Result) map[string][]Company {
	groups := map[string][]Company{}
	for _, c := range r.Companies {
		key := collapseKey(c.Name)
		groups[key] = append(groups[key], c)
	}
	return groups
}

// collapseKey normalises a company name to the key share classes are grouped by.
func collapseKey(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	lastHyphen := false
	for _, rune := range strings.ToLower(name) {
		switch {
		case (rune >= 'a' && rune <= 'z') || (rune >= '0' && rune <= '9'):
			b.WriteRune(rune)
			lastHyphen = false
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteRune('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
