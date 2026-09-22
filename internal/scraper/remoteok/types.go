// types.go: the wire shape of one RemoteOK job element.
//
// There is deliberately no Response/[]Job type: the response is a heterogeneous
// array whose first element is a legal notice, so callers parse into
// []json.RawMessage and decide per element (client.Parse, normalize.go).
//
// Every field below is a provisional guess at the wire format. None of it has
// been checked against a real payload, because no sample has been collected yet.
// The mapping doc is the source of truth once it exists.
package remoteok

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Job is one element of the RemoteOK response array.
//
// design-gap: the build spec asked for a field and a method both named ID. Go
// forbids that on one type, and an unexported field carrying a json tag is
// rejected by go vet, so the raw identifier field is exported as IDRaw and the
// string accessor is the ID() method. The json tag is unchanged, so wire decoding
// is exactly what the spec asked for.
type Job struct {
	// IDRaw is the candidate stable, source-unique per-job identifier, exactly as
	// it appeared on the wire. Read the string form with ID().
	// TODO(mapping): confirm against docs/remoteok-mapping.md - the open question
	// is whether this field is always present, always a string or sometimes a
	// number, and stable across runs when a listing is edited.
	IDRaw json.RawMessage `json:"id"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Slug string `json:"slug"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Company string `json:"company"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Title string `json:"position"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Description string `json:"description"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Location string `json:"location"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Tags []string `json:"tags"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	URL string `json:"url"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	ApplyURL string `json:"apply_url"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	CompanyLogo string `json:"company_logo"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Date string `json:"date"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	Epoch int64 `json:"epoch"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	SalaryMin json.RawMessage `json:"salary_min"`

	// TODO(mapping): confirm against docs/remoteok-mapping.md
	SalaryMax json.RawMessage `json:"salary_max"`
}

// ID returns the job's stable per-job identifier as a string. It accepts either
// a JSON string or a JSON number, because the dump has not yet told us which one
// the API uses, and returns an error when the field is absent, null, or empty.
//
// This is the value the normalizer will eventually populate the run's seen set
// from. There is deliberately no fallback: an element with no usable identifier
// must be reported as unusable rather than guessed at, so that it can never be
// inserted with a NULL external_id (design: Idempotency contract, Precondition).
func (j Job) ID() (string, error) {
	raw := j.IDRaw
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("remoteok: job has no id field")
	}

	// A quoted string is the expected shape.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return "", fmt.Errorf("remoteok: job id is an empty string")
		}
		return s, nil
	}

	// A bare number is accepted because the wire type is unconfirmed. Decoding
	// into json.Number keeps the literal text, so "007" is not silently turned
	// into "7".
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return "", fmt.Errorf("remoteok: job id is neither a string nor a number: %w", err)
	}
	if n.String() == "" {
		return "", fmt.Errorf("remoteok: job id is empty")
	}
	return n.String(), nil
}
