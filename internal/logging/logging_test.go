package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func int64Ptr(v int64) *int64 { return &v }

// decode reads one JSON log line.
func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("no log line written")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &out); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, lines[0])
	}
	return out
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestNewTraceIDIsAW3CWidthHexString(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewTraceID()
		if !hex32.MatchString(id) {
			t.Fatalf("trace id %q is not 32 lowercase hex characters", id)
		}
		if seen[id] {
			t.Fatalf("trace id %q repeated", id)
		}
		seen[id] = true
	}
}

func TestParseTraceparent(t *testing.T) {
	valid := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if got, ok := ParseTraceparent(valid); !ok || got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("ParseTraceparent(%q) = %q, %v", valid, got, ok)
	}
	for _, bad := range []string{"", "00", "00-short-00f067aa0ba902b7-01", "00-ZZZ92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"} {
		if got, ok := ParseTraceparent(bad); ok {
			t.Errorf("ParseTraceparent(%q) = %q, want not ok", bad, got)
		}
	}
}

func TestNewTraceparentRoundTrips(t *testing.T) {
	traceID := NewTraceID()
	header := NewTraceparent(traceID)
	if !strings.HasPrefix(header, "00-"+traceID+"-") || !strings.HasSuffix(header, "-01") {
		t.Fatalf("traceparent %q does not carry the trace id", header)
	}
	if got, ok := ParseTraceparent(header); !ok || got != traceID {
		t.Errorf("round trip gave %q, %v", got, ok)
	}
}

func TestNewTraceparentRepairsABadTraceID(t *testing.T) {
	header := NewTraceparent("nonsense")
	got, ok := ParseTraceparent(header)
	if !ok || !hex32.MatchString(got) {
		t.Errorf("traceparent %q did not end up valid", header)
	}
}

func TestIdentityEnvRoundTrips(t *testing.T) {
	original := Identity{TraceID: "a" + strings.Repeat("b", 31), SweepID: int64Ptr(457), RunID: int64Ptr(615), Company: "abbott-laboratories"}
	for _, entry := range original.Env() {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	got := IdentityFromEnv()
	if got.TraceID != original.TraceID || got.Company != original.Company {
		t.Errorf("trace/company = %q/%q, want %q/%q", got.TraceID, got.Company, original.TraceID, original.Company)
	}
	if got.SweepID == nil || *got.SweepID != 457 || got.RunID == nil || *got.RunID != 615 {
		t.Errorf("ids = %v/%v, want 457/615", got.SweepID, got.RunID)
	}
}

func TestIdentityFromEnvGeneratesWhenAbsent(t *testing.T) {
	t.Setenv(EnvTraceID, "")
	t.Setenv(EnvSweepID, "not-a-number")
	got := IdentityFromEnv()
	if !hex32.MatchString(got.TraceID) {
		t.Errorf("trace id = %q, want a generated one", got.TraceID)
	}
	if got.SweepID != nil {
		t.Errorf("sweep id = %v, want nil for an unparseable value", *got.SweepID)
	}
}

func TestLoggerEmitsTheReservedFields(t *testing.T) {
	var buf bytes.Buffer
	logger := New(
		Config{App: "jobs-app", Service: "scrape", Writer: &buf},
		Identity{TraceID: NewTraceID(), SweepID: int64Ptr(457), RunID: int64Ptr(615), Company: "acme"},
	)
	logger.Info("fetch", slog.Int("page_links", 42))

	line := decode(t, &buf)
	for key, want := range map[string]any{
		"app": "jobs-app", "svc": "scrape", "msg": "fetch",
		"company": "acme", "page_links": float64(42),
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	if line["run_id"] != float64(615) || line["sweep_id"] != float64(457) {
		t.Errorf("ids = %v/%v, want 615/457", line["run_id"], line["sweep_id"])
	}
	if line["trace_id"] == "" || line["trace_id"] == nil {
		t.Error("trace_id is missing")
	}
	if _, ok := line["ts"].(string); !ok {
		t.Errorf("ts = %v, want an RFC3339 string", line["ts"])
	}
	if line["level"] != "info" {
		t.Errorf("level = %v, want info (lowercase, for label matching)", line["level"])
	}
}

func TestLoggerOmitsAbsentIdentityFields(t *testing.T) {
	var buf bytes.Buffer
	New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()}).
		Info("started")

	line := decode(t, &buf)
	for _, key := range []string{"run_id", "sweep_id", "company", "span", "span"} {
		if _, present := line[key]; present {
			t.Errorf("%s should be omitted when unset, so 0 and 'not applicable' cannot be confused", key)
		}
	}
}

func TestLoggerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{App: "a", Service: "b", Level: "warn", Writer: &buf}, Identity{TraceID: NewTraceID()})
	logger.Info("quiet")
	logger.Warn("loud")

	out := buf.String()
	if strings.Contains(out, "quiet") {
		t.Error("info was written at level warn")
	}
	if !strings.Contains(out, "loud") {
		t.Error("warn was filtered out at level warn")
	}
}

func TestWithSpanAndNarrowingKeepTheTrace(t *testing.T) {
	var buf bytes.Buffer
	base := New(Config{App: "jobs-app", Service: "batch", Writer: &buf}, Identity{TraceID: NewTraceID(), SweepID: int64Ptr(457)})
	child := base.With(base.Identity().With(int64Ptr(615), "acme").WithSpan("agent"))
	child.Info("step")

	line := decode(t, &buf)
	if line["span"] != "agent" || line["run_id"] != float64(615) || line["company"] != "acme" {
		t.Errorf("child line = %v", line)
	}
	if line["trace_id"] != base.Identity().TraceID {
		t.Errorf("trace_id changed: %v != %v", line["trace_id"], base.Identity().TraceID)
	}
	if line["sweep_id"] != float64(457) {
		t.Errorf("sweep_id = %v, want the parent's 457", line["sweep_id"])
	}
}

func TestMiddlewareAdoptsAnIncomingTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()})

	traceID := NewTraceID()
	var seen Identity
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodPost, "/api/pipeline/batch", nil)
	request.Header.Set("traceparent", NewTraceparent(traceID))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if seen.TraceID != traceID {
		t.Errorf("handler saw trace %q, want the caller's %q", seen.TraceID, traceID)
	}
	if recorder.Header().Get(TraceHeader) != traceID {
		t.Errorf("response trace header = %q, want %q", recorder.Header().Get(TraceHeader), traceID)
	}
	line := decode(t, &buf)
	if line["trace_id"] != traceID {
		t.Errorf("logged trace_id = %v, want %q", line["trace_id"], traceID)
	}
	if line["status"] != float64(http.StatusTeapot) || line["path"] != "/api/pipeline/batch" {
		t.Errorf("request line = %v", line)
	}
}

func TestMiddlewareStartsATraceWhenNoneIsSent(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()})
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/runs", nil))

	generated := recorder.Header().Get(TraceHeader)
	if !hex32.MatchString(generated) {
		t.Fatalf("response trace header = %q, want a generated id", generated)
	}
	// No log line, and that is the point of the test's neighbour: a successful poll is not recorded, but
	// it is still traced, so a caller can follow the request into whatever it goes on to trigger.
	if buf.Len() != 0 {
		t.Errorf("a successful GET wrote %d bytes, want none: %s", buf.Len(), buf.String())
	}
}

func TestMiddlewareAcceptsTheSimpleHeader(t *testing.T) {
	logger := Discard()
	traceID := NewTraceID()
	var seen Identity
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(TraceHeader, traceID)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if seen.TraceID != traceID {
		t.Errorf("trace = %q, want %q", seen.TraceID, traceID)
	}
}

func TestFromContextWithoutAnIdentityStillProducesOne(t *testing.T) {
	if got := FromContext(context.Background()); !hex32.MatchString(got.TraceID) {
		t.Errorf("trace = %q, want a generated id", got.TraceID)
	}
}

func TestNotableRequestKeepsWhatCanExplainSomething(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	post := httptest.NewRequest(http.MethodPost, "/api/pipeline/batch", nil)
	head := httptest.NewRequest(http.MethodHead, "/", nil)

	cases := []struct {
		name    string
		req     *http.Request
		status  int
		elapsed time.Duration
		logAll  bool
		want    bool
		why     string
	}{
		{
			// The noise: 98.9% of the store was this. Nothing about a successful, instant poll can
			// explain anything.
			name: "a successful fast poll is dropped", req: get, status: 200, elapsed: 2 * time.Millisecond,
			want: false,
		},
		{
			name: "a state change is kept", req: post, status: 202, elapsed: time.Millisecond,
			want: true, why: "state-changing",
		},
		{
			// The POST above is also the first line of a trace_timeline; dropping reads must not touch it.
			name: "a failing read is kept", req: get, status: 500, elapsed: time.Millisecond,
			want: true, why: "failed",
		},
		{
			name: "a 404 is kept", req: get, status: 404, elapsed: time.Millisecond,
			want: true, why: "failed",
		},
		{
			name: "a slow read is kept", req: get, status: 200, elapsed: 900 * time.Millisecond,
			want: true, why: "slow",
		},
		{
			// The threshold is a boundary worth pinning: just under is polling, at is notable.
			name: "exactly at the threshold counts as slow", req: get, status: 200, elapsed: slowRequest,
			want: true, why: "slow",
		},
		{
			name: "a HEAD poll is dropped too", req: head, status: 200, elapsed: time.Millisecond,
			want: false,
		},
		{
			name: "JOBS_LOG_REQUESTS=all restores everything", req: get, status: 200, elapsed: time.Millisecond,
			logAll: true, want: true, why: "all-requests-enabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := notableRequest(tc.req, tc.status, tc.elapsed, tc.logAll)
			if got != tc.want {
				t.Errorf("notableRequest(...) = %v, want %v", got, tc.want)
			}
			if tc.want && why != tc.why {
				t.Errorf("reason = %q, want %q", why, tc.why)
			}
		})
	}
}

func TestMiddlewareStaysSilentOnAPollButKeepsTheTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()})

	traceID := NewTraceID()
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	request.Header.Set(TraceHeader, traceID)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if buf.Len() != 0 {
		t.Errorf("a successful poll wrote %d bytes, want none: %s", buf.Len(), buf.String())
	}
	// Silence about the record, not about the correlation: the trace is still adopted and returned so a
	// caller can follow its own request through everything it triggers.
	if got := recorder.Header().Get(TraceHeader); got != traceID {
		t.Errorf("response trace header = %q, want %q", got, traceID)
	}
}

func TestMiddlewareLogsAStateChangeWithItsReason(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()})
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/pipeline/batch", nil))

	line := decode(t, &buf)
	if line["path"] != "/api/pipeline/batch" || line["status"] != float64(http.StatusAccepted) {
		t.Errorf("line = %v", line)
	}
	if line["logged_because"] != "state-changing" {
		t.Errorf("logged_because = %v, want state-changing", line["logged_because"])
	}
}

func TestNoFieldIsWrittenTwice(t *testing.T) {
	// The regression: New attached trace_id, and then With attached it again, so every request line
	// carried two trace_id keys with two different values - and which one a reader believed depended on
	// its JSON parser rather than on this code. Counting the raw string is the only assertion that
	// catches a duplicate key, because decoding into a map silently keeps one.
	var buf bytes.Buffer
	base := New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()})
	narrowed := base.With(Identity{TraceID: NewTraceID(), Span: "agent", Company: "acme"})
	narrowed.Info("request")

	raw := buf.String()
	for _, key := range []string{"trace_id", "app", "svc"} {
		if got := strings.Count(raw, `"`+key+`"`); got != 1 {
			t.Errorf("%q appears %d times in one line, want exactly 1:\n%s", key, got, raw)
		}
	}
}

func TestNarrowingStillReplacesTheTraceWithoutDuplicatingIt(t *testing.T) {
	var buf bytes.Buffer
	base := New(Config{App: "jobs-app", Service: "server", Writer: &buf}, Identity{TraceID: NewTraceID()})
	requestTrace := NewTraceID()
	base.With(Identity{TraceID: requestTrace}).Info("request")

	line := decode(t, &buf)
	if line["trace_id"] != requestTrace {
		t.Errorf("trace_id = %v, want the request's %q", line["trace_id"], requestTrace)
	}
	if line["app"] != "jobs-app" || line["svc"] != "server" {
		t.Errorf("app/svc = %v/%v, want jobs-app/server", line["app"], line["svc"])
	}
}
