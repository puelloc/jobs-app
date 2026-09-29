// Command listings writes browser-extracted job postings into job_listings.
//
// It is the glue between worker/listings_fetch.py (which renders a filtered listings URL and pulls
// out each posting's URL, description and embedded JSON) and store.UpsertBrowserJob. It reads the
// listings_fetch JSON, keys every posting by (vendor platform id, external_id), and upserts it.
//
// Dry-run by default: it prints every row it would write and writes nothing. Pass -commit to write.
// A write to the live database is an explicit act, never the default.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/store"
)

type jobRecord struct {
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	RawData      string  `json:"raw_data"`
	Error        string  `json:"error"`
	Country      *string `json:"country"`
	LocationText *string `json:"location_text"`
	IsUS         *bool   `json:"is_us"`
}

type listingsOutput struct {
	ListingsURL string      `json:"listings_url"`
	Jobs        []jobRecord `json:"jobs"`
}

type playbook struct {
	ExternalIDPattern string `json:"external_id_pattern"`
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func main() { os.Exit(run()) }

func run() int {
	file := flag.String("file", "", "path to listings_fetch.py JSON output (required)")
	slug := flag.String("slug", "", "company slug (required)")
	vendor := flag.String("vendor", "", "vendor name: eightfold, phenom, ... (required)")
	playbookPath := flag.String("playbook", "", "vendor playbook path (default worker/vendor-playbooks/<vendor>.json)")
	commit := flag.Bool("commit", false, "actually write (default is a read-only dry run)")
	flag.Parse()

	if *file == "" || *slug == "" || *vendor == "" {
		fmt.Fprintln(os.Stderr, "listings: -file, -slug and -vendor are required")
		flag.Usage()
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "listings: config: %v\n", err)
		return 1
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listings: open database %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer database.Close()

	ctx := context.Background()

	companyID, err := store.CompanyIDBySlug(ctx, database, *slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listings: company %q: %v\n", *slug, err)
		return 1
	}
	platformID, err := store.PlatformIDByName(ctx, database, *vendor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listings: vendor %q: %v\n", *vendor, err)
		return 1
	}

	extPattern := ""
	if *playbookPath == "" {
		*playbookPath = fmt.Sprintf("worker/vendor-playbooks/%s.json", *vendor)
	}
	if raw, err := os.ReadFile(*playbookPath); err == nil {
		var pb playbook
		if json.Unmarshal(raw, &pb) == nil {
			extPattern = pb.ExternalIDPattern
		}
	} else {
		fmt.Fprintf(os.Stderr, "listings: no playbook at %s (using URL as external_id): %v\n", *playbookPath, err)
	}

	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listings: read %s: %v\n", *file, err)
		return 1
	}
	var out listingsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Fprintf(os.Stderr, "listings: parse %s: %v\n", *file, err)
		return 1
	}

	inserted, refreshed, skipped := 0, 0, 0
	for _, j := range out.Jobs {
		if j.URL == "" || j.Error != "" {
			continue
		}
		// US-only: keep a posting only when the extracted ld+json positively identifies it as
		// United States. "Unknown" is not US, so it is skipped too. The "remote" half is the
		// flow's own guarantee - this command only ever scrapes a remote-filtered listings URL.
		if j.IsUS == nil || !*j.IsUS {
			fmt.Fprintf(os.Stderr, "skip non-US %s\n", j.URL)
			skipped++
			continue
		}
		job := store.BrowserJob{
			ExternalID:   store.ExternalID(extPattern, j.URL),
			ListingURL:   j.URL,
			Title:        j.Title,
			Description:  j.Description,
			RawData:      j.RawData,
			LocationText: strOrEmpty(j.LocationText),
			Country:      strOrEmpty(j.Country),
			IsUS:         j.IsUS,
			IsRemote:     true,
		}
		if !*commit {
			fmt.Printf("DRY-RUN company=%d platform=%d ext_id=%s title=%q\n",
				companyID, platformID, job.ExternalID, job.Title)
			continue
		}
		_, isNew, err := store.UpsertBrowserJob(ctx, database, job, companyID, platformID, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "listings: upsert %s: %v\n", j.URL, err)
			return 1
		}
		if isNew {
			inserted++
		} else {
			refreshed++
		}
	}

	if *commit {
		fmt.Printf("wrote %d inserted, %d refreshed, %d skipped\n", inserted, refreshed, skipped)
	} else {
		fmt.Printf("dry run: %d jobs, %d skipped, nothing written (pass -commit to write)\n", len(out.Jobs), skipped)
	}
	return 0
}
