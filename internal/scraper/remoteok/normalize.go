// normalize.go: the normalization step (design: Run lifecycle step 7).
//
// THIS STEP IS A DELIBERATE STUB. Every other step of the documented lifecycle is
// implemented for real; this one deliberately accepts zero jobs, because the
// field mapping cannot be decided until a live payload has been collected and
// docs/remoteok-mapping.md exists.
//
// The signature is the real one, so the surrounding flow (seen set, both
// stale-marking guards, run finish, exit code) is exercised end to end.
package remoteok

import "encoding/json"

// NormalizedJob is a placeholder for the normalized form of one job element.
type NormalizedJob struct {
	// TODO(mapping): expand after docs/remoteok-mapping.md exists.
	ExternalID string
	// TODO(mapping): expand after docs/remoteok-mapping.md exists.
	Title string
}

// NormalizeSkip records one element that produced no normalized row.
type NormalizeSkip struct {
	// Index is the element's position in the parsed response array.
	Index int
	// Reason is a short, log-safe explanation. It must never contain job text
	// (design: Observability / Not logged).
	Reason string
}

// Normalize converts parsed response elements into rows ready for upsert, and
// returns the run's seen set.
//
// STUB: this implementation performs no parsing and no field extraction. It
// returns an empty jobs slice, an empty seenIDs map, and one NormalizeSkip per
// element with Reason "normalizer not implemented". No element is inspected, so
// no mapping decision is implied.
//
// The real implementation must:
//   - extract each element's identifier into seenIDs before any full
//     normalization, keeping the entry even when the row is later rejected
//     (design: Run lifecycle step 7), and
//   - return every element it cannot turn into a row as a NormalizeSkip.
func Normalize(elements []json.RawMessage) (jobs []NormalizedJob, seenIDs map[string]struct{}, skipped []NormalizeSkip, err error) {
	jobs = []NormalizedJob{}
	seenIDs = map[string]struct{}{}
	skipped = make([]NormalizeSkip, 0, len(elements))

	for i := range elements {
		skipped = append(skipped, NormalizeSkip{
			Index:  i,
			Reason: "normalizer not implemented",
		})
	}

	return jobs, seenIDs, skipped, nil
}
