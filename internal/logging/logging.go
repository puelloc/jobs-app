// Package logging emits the structured, correlatable log lines the observability stack indexes.
//
// The pipeline already writes human-readable reports to stdout ("run_id=615 company=acme found=3
// ..."). Those stay exactly as they are: they are the run's report, the UI shows them, and tests
// assert on them. This package is the other half - one JSON object per event on stderr - so a central
// collector can group by trace_id, sweep_id, run_id and company instead of by which file a line
// happened to land in.
//
// That last part is not hypothetical. During a sweep, `batch` runs each company's `scrape` as a child
// process, which inherits the batch's stdout: the child's own log file is therefore empty and the
// whole sweep's output sits in one file. A line's correlation fields are what make it attributable at
// all, which is why they are attached to every line rather than left to the reader.
//
// The field names are deliberately boring and stable: app, svc, trace_id, span, sweep_id, run_id,
// company, listing_id, application_id, console_run_id. An absent value is omitted, never zero, so
// "not applicable" and "id 0" cannot be confused.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables that carry correlation into a child process, and that a process reads to
// adopt the identity it was started with.
const (
	EnvTraceID = "JOBS_TRACE_ID"
	EnvSweepID = "JOBS_SWEEP_ID"
	EnvRunID   = "JOBS_RUN_ID"
	EnvCompany = "JOBS_COMPANY"
	EnvLevel   = "JOBS_LOG_LEVEL"
	EnvApp     = "JOBS_APP"
	EnvService = "JOBS_SERVICE"
	// EnvRequests widens request logging back to every request. The default records only what can
	// explain something; "all" is for the fifteen minutes you spend debugging the UI itself.
	EnvRequests = "JOBS_LOG_REQUESTS"
)

// slowRequest is the threshold above which a read is worth recording even though it succeeded: a GET
// that took half a second is a signal, a GET that took two milliseconds is the UI polling.
const slowRequest = 500 * time.Millisecond

// TraceHeader is the simple header, alongside the standard W3C one, so a shell or a service that has
// no traceparent support can still join the chain.
const TraceHeader = "X-Trace-Id"

// Identity is the correlation carried by one unit of work. It is attached to every line the logger
// writes, so a query never has to guess which run or company a message belongs to.
type Identity struct {
	// TraceID is the unit of work - one sweep, one HTTP request, one console action - propagated to
	// whatever that work spawns. 32 hex characters, the width W3C trace context uses, so an
	// OpenTelemetry collector can adopt it later without a migration.
	TraceID string
	// Span is the phase within the trace ("agent", "fetch", "store"), which is what turns a pile of
	// lines into a timeline.
	Span string
	// The pipeline's own identifiers, carried as fields rather than folded into the trace id, so both
	// "this one attempt" and "everything about this company, ever" are single queries.
	SweepID       *int64
	RunID         *int64
	Company       string
	ListingID     *int64
	ApplicationID *int64
	ConsoleRunID  *int64
}

// NewTraceID returns a fresh 32-hex-character trace id.
func NewTraceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// A trace id is diagnostics, not correctness: fall back to the clock rather than fail the run.
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// ParseTraceparent reads a W3C traceparent header ("00-<32 hex>-<16 hex>-01") and returns its trace
// id. A malformed header is ignored rather than rejected: losing correlation is better than refusing
// a request.
func ParseTraceparent(header string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) < 2 {
		return "", false
	}
	traceID := strings.ToLower(parts[1])
	if len(traceID) != 32 || !isHex(traceID) {
		return "", false
	}
	return traceID, true
}

// NewTraceparent renders a traceparent for propagation, with a fresh span id.
func NewTraceparent(traceID string) string {
	if len(traceID) != 32 || !isHex(traceID) {
		traceID = NewTraceID()
	}
	var span [8]byte
	if _, err := rand.Read(span[:]); err != nil {
		return "00-" + traceID + "-0000000000000001-01"
	}
	return "00-" + traceID + "-" + hex.EncodeToString(span[:]) + "-01"
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// IdentityFromEnv adopts whatever correlation the process was started with, generating a trace id when
// it was started with none - a command run by hand is still one unit of work worth grouping.
func IdentityFromEnv() Identity {
	id := Identity{
		TraceID: os.Getenv(EnvTraceID),
		Company: os.Getenv(EnvCompany),
		SweepID: int64FromEnv(EnvSweepID),
		RunID:   int64FromEnv(EnvRunID),
	}
	if id.TraceID == "" {
		id.TraceID = NewTraceID()
	}
	return id
}

func int64FromEnv(name string) *int64 {
	value := os.Getenv(name)
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

// Env renders the identity as environment entries for a child process, which is how a sweep's
// companies inherit the sweep's trace id.
func (i Identity) Env() []string {
	env := []string{EnvTraceID + "=" + i.TraceID}
	if i.SweepID != nil {
		env = append(env, fmt.Sprintf("%s=%d", EnvSweepID, *i.SweepID))
	}
	if i.RunID != nil {
		env = append(env, fmt.Sprintf("%s=%d", EnvRunID, *i.RunID))
	}
	if i.Company != "" {
		env = append(env, EnvCompany+"="+i.Company)
	}
	return env
}

// WithSpan returns the identity with its phase set.
func (i Identity) WithSpan(span string) Identity {
	i.Span = span
	return i
}

// With returns the identity with the given run, company and sweep replaced, for work that narrows to
// one company within a sweep.
func (i Identity) With(runID *int64, company string) Identity {
	i.RunID = runID
	i.Company = company
	return i
}

// App names the application for the `app` label, so all three projects' logs land in one store under
// distinguishable names: JOBS_APP, or "jobs-app".
func App() string {
	if value := os.Getenv(EnvApp); value != "" {
		return value
	}
	return "jobs-app"
}

// Service names the process within the app for the `svc` label: JOBS_SERVICE, or the fallback.
func Service(fallback string) string {
	if value := os.Getenv(EnvService); value != "" {
		return value
	}
	return fallback
}

// Config describes one process's logger.
type Config struct {
	App     string
	Service string
	Level   string
	Writer  io.Writer
}

// Logger writes JSON lines with a base identity attached to each.
type Logger struct {
	// handler is the bare JSON handler, and every derived logger is built from it rather than from a
	// logger that already carries an identity. Chaining .With() twice attached trace_id twice, which
	// produced JSON with two trace_id keys - carrying two *different* values, so which one a reader saw
	// was an accident of that reader's parser.
	handler slog.Handler
	app     string
	service string
	id      Identity
	inner   *slog.Logger
}

// New builds a logger. The writer is usually stderr: stdout is the run's human-readable report, and
// keeping telemetry off it means the reports the UI shows and the tests assert on are unchanged.
func New(cfg Config, id Identity) *Logger {
	writer := cfg.Writer
	if writer == nil {
		writer = os.Stderr
	}
	level := parseLevel(cfg.Level)
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) != 0 {
				return attr
			}
			switch attr.Key {
			case slog.TimeKey:
				// "ts", and always UTC RFC3339 with milliseconds: the same instant format the schema
				// and the traces already use, so a log line sorts against them as text.
				attr.Key = "ts"
				attr.Value = slog.StringValue(attr.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
			case slog.LevelKey:
				attr.Key = "level"
				if lvl, ok := attr.Value.Any().(slog.Level); ok {
					attr.Value = slog.StringValue(strings.ToLower(lvl.String()))
				}
			}
			return attr
		},
	})

	base := &Logger{handler: handler, app: cfg.App, service: cfg.Service}
	return base.withIdentityAttrs(id)
}

// withIdentityAttrs attaches the identity, inheriting the receiver's trace when the given one is
// empty. trace_id is attached here rather than in New so that narrowing to another trace actually
// changes the output instead of silently keeping the parent's.
func (l *Logger) withIdentityAttrs(id Identity) *Logger {
	if id.TraceID == "" {
		id.TraceID = l.id.TraceID
	}
	if id.TraceID == "" {
		id.TraceID = NewTraceID()
	}
	attrs := []any{
		slog.String("app", l.app),
		slog.String("svc", l.service),
		slog.String("trace_id", id.TraceID),
	}
	if id.Span != "" {
		attrs = append(attrs, slog.String("span", id.Span))
	}
	if id.SweepID != nil {
		attrs = append(attrs, slog.Int64("sweep_id", *id.SweepID))
	}
	if id.RunID != nil {
		attrs = append(attrs, slog.Int64("run_id", *id.RunID))
	}
	if id.Company != "" {
		attrs = append(attrs, slog.String("company", id.Company))
	}
	if id.ListingID != nil {
		attrs = append(attrs, slog.Int64("listing_id", *id.ListingID))
	}
	if id.ApplicationID != nil {
		attrs = append(attrs, slog.Int64("application_id", *id.ApplicationID))
	}
	if id.ConsoleRunID != nil {
		attrs = append(attrs, slog.Int64("console_run_id", *id.ConsoleRunID))
	}
	return &Logger{
		handler: l.handler,
		app:     l.app,
		service: l.service,
		id:      id,
		inner:   slog.New(l.handler).With(attrs...),
	}
}

// With returns a logger for a narrower identity, sharing the handler. An empty TraceID inherits the
// receiver's, so narrowing to a run keeps the sweep's trace.
func (l *Logger) With(id Identity) *Logger {
	return l.withIdentityAttrs(id)
}

// Identity returns the logger's current identity.
func (l *Logger) Identity() Identity { return l.id }

// Enabled reports whether a level would be written, so a caller can skip building an expensive
// argument.
func (l *Logger) Enabled(level slog.Level) bool { return l.inner.Enabled(context.Background(), level) }

func (l *Logger) Debug(msg string, args ...any) { l.inner.Debug(msg, args...) }
func (l *Logger) Info(msg string, args ...any)  { l.inner.Info(msg, args...) }
func (l *Logger) Warn(msg string, args ...any)  { l.inner.Warn(msg, args...) }
func (l *Logger) Error(msg string, args ...any) { l.inner.Error(msg, args...) }

// Discard returns a logger that writes nothing, for tests that do not assert on output.
func Discard() *Logger {
	return &Logger{
		handler: slog.NewJSONHandler(io.Discard, nil),
		app:     "discard",
		service: "discard",
		id:      Identity{TraceID: NewTraceID()},
		inner:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ---------------------------------------------------------------- request correlation

type contextKey struct{}

// WithIdentity puts a trace identity on a request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext reads the identity, or a fresh one when the context carries none.
func FromContext(ctx context.Context) Identity {
	if id, ok := ctx.Value(contextKey{}).(Identity); ok && id.TraceID != "" {
		return id
	}
	return Identity{TraceID: NewTraceID()}
}

// IdentityFromRequest adopts a trace from an incoming request - the W3C header first, then the simple
// one - or starts a new trace when the caller sent neither.
func IdentityFromRequest(r *http.Request) Identity {
	if traceID, ok := ParseTraceparent(r.Header.Get("traceparent")); ok {
		return Identity{TraceID: traceID}
	}
	if traceID := strings.ToLower(strings.TrimSpace(r.Header.Get(TraceHeader))); len(traceID) == 32 && isHex(traceID) {
		return Identity{TraceID: traceID}
	}
	return Identity{TraceID: NewTraceID()}
}

// statusRecorder remembers the status code so the request can be logged once with its outcome.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Middleware gives every request a trace id, echoes it back for the caller to correlate with, and logs
// one line per request with its method, path, status and duration. It is what joins a UI action to the
// job it triggered: the triggered child inherits this trace.
func Middleware(logger *Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromRequest(r)
		// A caller that already has a trace keeps it; one that does not learns it from the response.
		w.Header().Set(TraceHeader, id.TraceID)
		if _, ok := ParseTraceparent(r.Header.Get("traceparent")); !ok {
			w.Header().Set("traceparent", NewTraceparent(id.TraceID))
		}

		requestLogger := logger.With(id)
		ctx := WithIdentity(r.Context(), id)
		started := time.Now()

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r.WithContext(ctx))

		elapsed := time.Since(started)
		keep, why := notableRequest(r, recorder.status, elapsed, os.Getenv(EnvRequests) == "all")
		if !keep {
			// Deliberately silent. The trace id was still assigned and echoed, so a caller that wants
			// this request followed can send it on - it is the *record* of a successful poll that carries
			// nothing, not the correlation.
			return
		}

		attrs := []any{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Int64("duration_ms", elapsed.Milliseconds()),
			slog.String("logged_because", why),
		}
		switch {
		case recorder.status >= http.StatusInternalServerError:
			// At a level an alert can key on without parsing the message.
			requestLogger.Error("request failed", attrs...)
		case why == "slow":
			requestLogger.Warn("request", attrs...)
		default:
			requestLogger.Info("request", attrs...)
		}
	})
}

// notableRequest reports whether a finished request is worth a log line, and why.
//
// The UI polls several endpoints every five seconds while a run page is open, and those lines were
// 98.9% of everything in the store - 2,078 of 2,102 lines in one hour, none of which said anything.
// That is not merely wasted disk: it buries the nineteen lines that month that explain a failure, and
// it makes every question slower to answer.
//
// What is kept is everything that can explain something:
//
//   - a request that changed state (a trigger, a stop, a pause) - and the POST that starts a sweep is
//     the first line of a trace_timeline, so losing it would break the story at its root
//   - a request that failed, which is the point of having request logs at all
//   - a request that was slow, which is a symptom even when it succeeded
func notableRequest(r *http.Request, status int, elapsed time.Duration, logAll bool) (bool, string) {
	if logAll {
		return true, "all-requests-enabled"
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true, "state-changing"
	}
	if status >= http.StatusBadRequest {
		return true, "failed"
	}
	if elapsed >= slowRequest {
		return true, "slow"
	}
	return false, ""
}
