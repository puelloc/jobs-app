// Package browseruse drives the Python browser-use worker from Go.
//
// The worker is the M3 escalation tier: a real browser renders a page that a plain HTTP client
// cannot read, and a browser-use agent navigates to the company's actual job listings when the
// stored URL is not one. Nothing the worker says is trusted. It reports what the browser saw, and
// the caller feeds that through internal/careers.Validate exactly as it does a deterministic tier's
// response.
//
// The seam is deliberately narrow and process-shaped, for the reason the plan gives: browser-use is
// nondeterministic, so it cannot sit on the tested path. Only the place its output is consumed can
// be pinned, and that place is here. Every test in this package runs a fake worker, so the suite
// needs neither a browser, a model host, nor a network.
//
// Contract: docs/browser-use-worker.md.
//
// design: docs/sp1500-plan.md, section 7.5.
package browseruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Mode selects what the worker does with the URL.
type Mode string

const (
	// ModeRender loads the URL in a real browser and returns what it saw. It is the primary tier.
	ModeRender Mode = "render"
	// ModeAgent runs the LLM navigation loop and returns the URL it ended on. It is the residue
	// tier, and its result is re-rendered before it is judged.
	ModeAgent Mode = "agent"
)

// ErrorKind values, mirroring the worker contract's error_kind vocabulary.
const (
	// KindConfig means a bad request field, which is a caller bug.
	KindConfig = "config"
	// KindLaunch means no browser could be started, which is an environment fault: every URL would
	// fail the same way.
	KindLaunch = "launch"
	// KindNavigation means the page could not be loaded: DNS, TLS, refused, or an error raised by
	// navigation. It is a fact about the site, not about the worker.
	KindNavigation = "navigation"
	// KindTimeout means the navigation or process budget expired.
	KindTimeout = "timeout"
	// KindAgent means the LLM loop failed. Escalation failing says nothing about the URL.
	KindAgent = "agent"
	// KindProtocol means the worker produced no usable response.
	KindProtocol = "protocol"
	// KindExit means the worker process exited non-zero without producing a response.
	KindExit = "exit"
)

// AgentOptions carry the model configuration for ModeAgent.
type AgentOptions struct {
	// Model is the Ollama model tag, e.g. "qwen3.8-27b-64k:latest".
	Model string
	// Host is the Ollama base URL. The worker field is named host because that is browser-use's own
	// keyword for it; see the contract.
	Host string
	// MaxSteps bounds the navigation loop.
	MaxSteps int
	// NumCtx is the model's context window. Zero leaves the worker's default.
	NumCtx int
	// KeepAlive pins the model in memory; -1 means forever, which a batch run wants.
	KeepAlive int
}

// Request is one worker invocation, matching the contract's request object.
type Request struct {
	Mode           Mode
	URL            string
	CompanyName    string
	TimeoutSeconds float64
	MaxBodyBytes   int64
	Agent          *AgentOptions
	// TraceFile, when non-empty, is a path the worker appends one JSON object per agent step to, so
	// a long agent run can be watched as it happens. The worker writes nothing when it is empty.
	TraceFile string
}

// Result is one worker response.
//
// OK is false for a site-level failure, which is an answer rather than an error: the site refused,
// timed out, or the agent could not navigate. ErrorKind then says which. A non-nil error from
// Render means something else entirely - the worker could not be run, or broke protocol - and no
// response was received.
type Result struct {
	OK           bool
	Mode         string
	RequestedURL string
	FinalURL     string
	Status       int
	ContentType  string
	Title        string
	Body         []byte
	BodyBytes    int64
	Truncated    bool
	Redirected   bool
	Note         string
	Error        string
	ErrorKind    string
}

// Renderer runs one request against a browser.
type Renderer interface {
	Render(ctx context.Context, req Request) (Result, error)
}

// Error is a run-level failure: the worker could not be trusted to produce an answer.
type Error struct {
	Kind    string
	Message string
	// Stderr is the tail of the worker's stderr, kept because a launch failure is usually explained
	// there and nowhere else.
	Stderr string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("browser-use worker %s: %s", e.Kind, e.Message)
	if e.Stderr != "" {
		msg += "; stderr: " + e.Stderr
	}
	return msg
}

// SubprocessWorker runs the Python worker as one process per request.
//
// One process per request is slower than a long-lived worker but needs no protocol beyond the
// contract, and it means a hung or crashed browser cannot poison the rest of a 600-URL sweep. The
// cost is a Chromium start per URL, which is bounded and measurable; a persistent worker is the
// optimization to reach for only if the measurement says so.
type SubprocessWorker struct {
	// Command is the worker invocation, e.g. {"python", "worker/browser_worker.py"}. It must not be
	// empty; a missing interpreter is a configuration error, not a site outcome.
	Command []string
	// Dir is the worker's working directory. Empty means the current directory.
	Dir string
	// Env holds extra environment entries, appended to the inherited environment.
	Env []string
	// Timeout bounds the whole process. Zero means the parent context alone decides. It maps to a
	// site-level timeout, not a protocol failure: a page that never finishes is a fact about the
	// page, and the run continues.
	Timeout time.Duration
	// Stderr receives the child's stderr. Nil discards it, which is what the gate tests want and
	// what a quiet batch run wants except on failure.
	Stderr io.Writer
	// MaxStdoutBytes bounds the captured response. The worker's response carries the rendered body,
	// so this must comfortably exceed MaxBodyBytes or a healthy page reads as a protocol error.
	MaxStdoutBytes int64
}

// defaultMaxStdoutBytes leaves room for the JSON envelope, its escaping, and a 1.5MB body.
const defaultMaxStdoutBytes = 16 << 20

// workerWaitDelay bounds how long the process's inherited I/O pipes may stay open after the process
// is killed. It is what makes a timeout actually fast; see the comment on cmd.WaitDelay.
const workerWaitDelay = 2 * time.Second

// Render runs the worker once.
//
// It returns (Result, nil) whenever the worker produced a response, including ok:false - a site
// that refuses us has answered. It returns a non-nil *Error only when the process could not be run,
// exited without a response, or produced something that is not the contract's single JSON object.
// That split is the same one the careers.Fetcher contract makes between a 403 and a dead host, and
// for the same reason: an unreachable site is unknown, not disproven, and the two must not be
// recorded as the same fact.
func (w *SubprocessWorker) Render(ctx context.Context, req Request) (Result, error) {
	if len(w.Command) == 0 {
		return Result{}, &Error{Kind: KindConfig, Message: "Command is empty"}
	}

	payload, err := json.Marshal(toWireRequest(req))
	if err != nil {
		return Result{}, &Error{Kind: KindConfig, Message: fmt.Sprintf("encode request: %v", err)}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if w.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, w.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, w.Command[0], w.Command[1:]...)
	cmd.Dir = w.Dir
	cmd.Env = append(cmd.Environ(), w.Env...)
	cmd.Stdin = bytes.NewReader(payload)
	// WaitDelay is load-bearing on the timeout path. CommandContext kills the process it started,
	// but that process is a shell (or Python) whose children - sleep, Chromium - inherit the stdout
	// pipe. Without a bound, Wait blocks until those grandchildren exit, so a 150ms budget can take
	// as long as the child's own lifetime: measured at 5s for a `sleep 5` stand-in before this was
	// added. WaitDelay closes the pipes and lets the sweep move on.
	cmd.WaitDelay = workerWaitDelay

	maxOut := w.MaxStdoutBytes
	if maxOut <= 0 {
		maxOut = defaultMaxStdoutBytes
	}
	stdout := &boundedBuffer{limit: maxOut}
	cmd.Stdout = stdout
	if w.Stderr != nil {
		cmd.Stderr = w.Stderr
	}

	runErr := cmd.Run()

	// A deadline is a page that would not finish, not a broken worker. It is reported the way the
	// contract reports it so the caller records a timeout attempt and moves on.
	if runCtx.Err() != nil && ctx.Err() == nil {
		return Result{
			OK:           false,
			Mode:         string(req.Mode),
			RequestedURL: req.URL,
			Error:        fmt.Sprintf("worker exceeded its %s budget", w.Timeout),
			ErrorKind:    KindTimeout,
		}, nil
	}
	if ctx.Err() != nil {
		return Result{}, &Error{Kind: KindProtocol, Message: fmt.Sprintf("run cancelled: %v", ctx.Err())}
	}

	// The response is parsed before the exit code is judged. The contract says a response implies
	// exit 0, but a worker that answers and *then* dies on cleanup has still answered, and throwing
	// that answer away would lose real observations for a reason unrelated to the site.
	res, decodeErr := decodeResponse(stdout.Bytes())
	if decodeErr == nil {
		return mapResult(res)
	}

	if runErr != nil {
		return Result{}, &Error{
			Kind:    KindExit,
			Message: fmt.Sprintf("exit: %v", runErr),
			Stderr:  "",
		}
	}
	if stdout.Truncated() {
		return Result{}, &Error{
			Kind:    KindProtocol,
			Message: fmt.Sprintf("worker stdout exceeded the %d-byte capture bound", maxOut),
		}
	}
	return Result{}, &Error{Kind: KindProtocol, Message: decodeErr.Error()}
}

// wireRequest is the on-the-wire shape. It is separate from Request so the JSON field names are
// pinned here rather than by struct tags that a refactor could rename unnoticed.
type wireRequest struct {
	Mode           string     `json:"mode"`
	URL            string     `json:"url"`
	CompanyName    string     `json:"company_name,omitempty"`
	TimeoutSeconds float64    `json:"timeout_seconds,omitempty"`
	MaxBodyBytes   int64      `json:"max_body_bytes,omitempty"`
	Agent          *wireAgent `json:"agent,omitempty"`
	TraceFile      string     `json:"trace_file,omitempty"`
}

type wireAgent struct {
	Model     string `json:"model,omitempty"`
	Host      string `json:"host,omitempty"`
	MaxSteps  int    `json:"max_steps,omitempty"`
	NumCtx    int    `json:"num_ctx,omitempty"`
	KeepAlive int    `json:"keep_alive,omitempty"`
}

func toWireRequest(req Request) wireRequest {
	out := wireRequest{
		Mode:           string(req.Mode),
		URL:            req.URL,
		CompanyName:    req.CompanyName,
		TimeoutSeconds: req.TimeoutSeconds,
		MaxBodyBytes:   req.MaxBodyBytes,
		TraceFile:      req.TraceFile,
	}
	if req.Agent != nil {
		out.Agent = &wireAgent{
			Model:     req.Agent.Model,
			Host:      req.Agent.Host,
			MaxSteps:  req.Agent.MaxSteps,
			NumCtx:    req.Agent.NumCtx,
			KeepAlive: req.Agent.KeepAlive,
		}
	}
	return out
}

// wireResponse is the on-the-wire response shape. Unknown fields are ignored so the worker can add
// one without a Go change, which is the direction that keeps the contract evolvable.
type wireResponse struct {
	OK           bool   `json:"ok"`
	Mode         string `json:"mode"`
	RequestedURL string `json:"requested_url"`
	FinalURL     string `json:"final_url"`
	Status       int    `json:"status"`
	ContentType  string `json:"content_type"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	BodyBytes    int64  `json:"body_bytes"`
	Truncated    bool   `json:"truncated"`
	Redirected   bool   `json:"redirected"`
	Note         string `json:"note"`
	Error        string `json:"error"`
	ErrorKind    string `json:"error_kind"`
}

// decodeResponse parses the worker's stdout as exactly one JSON object.
//
// json.Unmarshal rejects trailing content, so a worker that prints a stray banner alongside its
// response fails here instead of being silently half-read. That is the point: stdout purity is a
// protocol requirement, and a violation has to surface as one.
func decodeResponse(raw []byte) (wireResponse, error) {
	var res wireResponse
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return res, errors.New("worker wrote no response to stdout")
	}
	if err := json.Unmarshal(trimmed, &res); err != nil {
		return res, fmt.Errorf("worker stdout is not one JSON object: %w (first 200 bytes: %q)",
			err, firstBytes(trimmed, 200))
	}
	return res, nil
}

// mapResult turns a decoded response into a Result, promoting the kinds that mean the worker
// itself is broken to a run-level error.
func mapResult(res wireResponse) (Result, error) {
	out := Result{
		OK:           res.OK,
		Mode:         res.Mode,
		RequestedURL: res.RequestedURL,
		FinalURL:     res.FinalURL,
		Status:       res.Status,
		ContentType:  res.ContentType,
		Title:        res.Title,
		Body:         []byte(res.Body),
		BodyBytes:    res.BodyBytes,
		Truncated:    res.Truncated,
		Redirected:   res.Redirected,
		Note:         res.Note,
		Error:        res.Error,
		ErrorKind:    res.ErrorKind,
	}

	if res.OK {
		// A successful render with nothing to gate is not usable evidence. Refusing it here means
		// the caller cannot mistake an empty response for a rejection of the page.
		if Mode(res.Mode) == ModeRender && strings.TrimSpace(res.FinalURL) == "" {
			return Result{}, &Error{Kind: KindProtocol, Message: "render succeeded with no final_url"}
		}
		return out, nil
	}

	switch res.ErrorKind {
	case KindNavigation, KindTimeout, KindAgent:
		// A site-level answer: the page did not arrive. The caller records it and continues.
		return out, nil
	case KindConfig, KindLaunch, KindProtocol:
		return Result{}, &Error{Kind: res.ErrorKind, Message: res.Error}
	case "":
		return Result{}, &Error{Kind: KindProtocol, Message: "worker reported failure with no error_kind"}
	default:
		// An unrecognised kind is a contract drift, not a site fact. Treating it as a site fact
		// would let a future worker failure be written into the attempts table as a verdict.
		return Result{}, &Error{
			Kind:    KindProtocol,
			Message: fmt.Sprintf("worker reported unknown error_kind %q: %s", res.ErrorKind, res.Error),
		}
	}
}

// boundedBuffer accumulates up to limit bytes and remembers that it truncated.
type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	if _, err := b.buf.Write(p); err != nil {
		return 0, err
	}
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// Truncated reports whether the capture hit its bound, which turns a decode failure into a clearer
// message than "invalid JSON".
func (b *boundedBuffer) Truncated() bool { return b.truncated }

func firstBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
