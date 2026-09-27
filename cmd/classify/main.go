// Command classify renders each company's stored careers URL and records which applicant-tracking
// vendor is behind it (on company_application_platforms). It is the wiring half of the vendor
// classifier: the pure classification is careers.Fingerprint and the write is
// store.SetCompanyApplicationPlatform, both unit-tested.
//
// Dry-run by default: it prints each company's vendor and writes nothing. Pass -commit to record.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"jobsapp/internal/browseruse"
	"jobsapp/internal/careers"
	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/runvalidate"
	"jobsapp/internal/store"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	onlySlugs := fs.String("only-slugs", "", "comma-separated company slugs to classify")
	limit := fs.Int("limit", 0, "cap the number of companies (0 means all)")
	pageTimeout := fs.Duration("page-timeout", 45*time.Second, "per-page render budget")
	workerTimeout := fs.Duration("worker-timeout", 90*time.Second, "whole worker process budget for one render")
	maxBodyBytes := fs.Int64("max-body-bytes", 1_500_000, "bound on the rendered body handed to the fingerprint")
	commit := fs.Bool("commit", false, "write classifications (default is a read-only dry run)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "classify: config: %v\n", err)
		return 1
	}
	if !cfg.BrowserUseEnabled {
		fmt.Fprintln(stderr, "classify: ENABLE_BROWSER_USE is not set to a true value")
		return 1
	}
	command := strings.Fields(cfg.BrowserUseCommand)
	if len(command) == 0 {
		fmt.Fprintln(stderr, "classify: BROWSER_WORKER_COMMAND names no command")
		return 1
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "classify: open database %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	companies, err := runvalidate.LoadStoredCompanies(ctx, database, runvalidate.StoredFilter{
		OnlySlugs: splitSlugs(*onlySlugs),
	})
	if err != nil {
		fmt.Fprintf(stderr, "classify: load companies: %v\n", err)
		return 1
	}
	if *limit > 0 && len(companies) > *limit {
		companies = companies[:*limit]
	}

	renderer := &browseruse.SubprocessWorker{
		Command:        command,
		Timeout:        *workerTimeout,
		Stderr:         stderr,
		MaxStdoutBytes: *maxBodyBytes*2 + (1 << 20),
	}

	classified, unknown, failed := 0, 0, 0
	for _, c := range companies {
		res, err := renderer.Render(ctx, browseruse.Request{
			Mode:           browseruse.ModeRender,
			URL:            c.CareerSiteURL,
			CompanyName:    c.Name,
			TimeoutSeconds: pageTimeout.Seconds(),
			MaxBodyBytes:   *maxBodyBytes,
		})
		if err != nil {
			fmt.Fprintf(stderr, "company=%s vendor=failed err=%q\n", c.Slug, err)
			failed++
			continue
		}
		if !res.OK {
			fmt.Fprintf(stdout, "company=%s vendor=unknown status=%d error=%q\n",
				c.Slug, res.Status, res.Error)
			unknown++
			continue
		}

		vendor := careers.Fingerprint(res.FinalURL, string(res.Body))
		if vendor == "" {
			fmt.Fprintf(stdout, "company=%s vendor=unknown url=%s\n", c.Slug, res.FinalURL)
			unknown++
			continue
		}
		if *commit {
			platformID, err := store.PlatformIDByName(ctx, database, vendor)
			if err != nil {
				fmt.Fprintf(stderr, "company=%s vendor=%s error=platform err=%q\n", c.Slug, vendor, err)
				failed++
				continue
			}
			if err := store.SetCompanyApplicationPlatform(ctx, database, c.ID, platformID, res.FinalURL); err != nil {
				fmt.Fprintf(stderr, "company=%s vendor=%s error=write err=%q\n", c.Slug, vendor, err)
				failed++
				continue
			}
		}
		fmt.Fprintf(stdout, "company=%s vendor=%s url=%s\n", c.Slug, vendor, res.FinalURL)
		classified++
	}

	fmt.Fprintf(stdout, "classified=%d unknown=%d failed=%d total=%d commit=%t\n",
		classified, unknown, failed, len(companies), *commit)
	return 0
}

func splitSlugs(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
