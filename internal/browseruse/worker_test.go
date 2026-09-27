package browseruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests in this file run shell-script stand-ins for the Python worker. That is the seam the
// plan asks to pin: browser-use is nondeterministic, so the suite proves the *contract* - what Go
// does with each shape of response - and never launches a browser or reaches a model host.

// scriptWorker writes body to a temporary shell script and returns a worker that runs it.
func scriptWorker(t *testing.T, body string) (*SubprocessWorker, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write worker script: %v", err)
	}
	stderr := &bytes.Buffer{}
	return &SubprocessWorker{Command: []string{"/bin/sh", path}, Stderr: stderr}, stderr
}

func TestRenderReturnsTheWorkersResult(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":true,"mode":"render","requested_url":"https://example.com/careers","final_url":"https://example.com/careers/","status":200,"content_type":"text/html; charset=utf-8","title":"Careers | Example","body":"<html><title>Careers | Example</title></html>","body_bytes":46,"truncated":false,"redirected":true,"note":""}'
`)

	res, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/careers"})
	if err != nil {
		t.Fatalf("Render returned an error for a successful worker: %v", err)
	}
	if !res.OK {
		t.Fatal("OK is false, want true")
	}
	if res.Status != 200 {
		t.Errorf("Status = %d, want 200", res.Status)
	}
	if res.FinalURL != "https://example.com/careers/" {
		t.Errorf("FinalURL = %q, want the redirect target", res.FinalURL)
	}
	if res.Title != "Careers | Example" {
		t.Errorf("Title = %q", res.Title)
	}
	if !strings.Contains(string(res.Body), "Careers | Example") {
		t.Errorf("Body = %q, want the rendered document", res.Body)
	}
	if !res.Redirected {
		t.Error("Redirected = false, want true")
	}
}

// The contract's field names are sent, not just parsed. A worker that silently receives
// "mode":"" would render nothing and report success.
func TestRenderSendsTheContractRequestShape(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "request.json")
	worker, _ := scriptWorker(t, `
cat > "$CAPTURE"
printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/","status":200}'
`)
	worker.Env = []string{"CAPTURE=" + capture}

	_, err := worker.Render(context.Background(), Request{
		Mode:           ModeRender,
		URL:            "https://example.com/careers",
		CompanyName:    "Example Corp",
		TimeoutSeconds: 12.5,
		MaxBodyBytes:   4096,
		Agent:          &AgentOptions{Model: "qwen3.8-27b-64k:latest", Host: "https://ai.example", MaxSteps: 6, NumCtx: 64440, KeepAlive: -1},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured request: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("captured request is not JSON: %v (%s)", err, raw)
	}
	if got["mode"] != "render" {
		t.Errorf(`mode = %v, want "render"`, got["mode"])
	}
	if got["url"] != "https://example.com/careers" {
		t.Errorf("url = %v", got["url"])
	}
	if got["company_name"] != "Example Corp" {
		t.Errorf("company_name = %v", got["company_name"])
	}
	if got["timeout_seconds"] != 12.5 {
		t.Errorf("timeout_seconds = %v, want 12.5", got["timeout_seconds"])
	}
	if got["max_body_bytes"] != float64(4096) {
		t.Errorf("max_body_bytes = %v, want 4096", got["max_body_bytes"])
	}
	agent, ok := got["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent = %v, want an object", got["agent"])
	}
	if agent["model"] != "qwen3.8-27b-64k:latest" || agent["host"] != "https://ai.example" || agent["max_steps"] != float64(6) {
		t.Errorf("agent = %v", agent)
	}
}

// A worker that wrote a usable response and then died on cleanup has still answered. Discarding it
// would lose a real observation for a reason that has nothing to do with the site.
func TestRenderUsesAResponseWrittenBeforeANonZeroExit(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/","status":200}'
exit 3
`)

	res, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("Render: %v, want the response to be used", err)
	}
	if !res.OK || res.Status != 200 {
		t.Errorf("res = %+v, want the written response", res)
	}
}

func TestRenderReportsANonZeroExitWithNoResponseAsRunLevelFailure(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
exit 7
`)

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	if err == nil {
		t.Fatal("Render returned no error for a worker that exited 7 without a response")
	}
	var werr *Error
	if !errors.As(err, &werr) {
		t.Fatalf("error is %T, want *browseruse.Error", err)
	}
	if werr.Kind != KindExit {
		t.Errorf("Kind = %q, want %q", werr.Kind, KindExit)
	}
}

func TestRenderRejectsStdoutThatIsNotOneJSONObject(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
echo 'this is not json'
`)

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindProtocol {
		t.Fatalf("err = %v, want a protocol error", err)
	}
	if !strings.Contains(werr.Message, "not one JSON object") {
		t.Errorf("message = %q, want it to name the protocol violation", werr.Message)
	}
}

// Two objects on stdout is the failure a stray print produces. Quietly reading the first one would
// let a worker's debug output be mistaken for the protocol working.
func TestRenderRejectsTwoJSONObjectsOnStdout(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s\n' '{"ok":true,"mode":"render","final_url":"https://example.com/","status":200}'
printf '%s\n' '{"ok":true,"mode":"render","final_url":"https://example.com/","status":200}'
`)

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindProtocol {
		t.Fatalf("err = %v, want a protocol error", err)
	}
}

// A page that will not load is a fact about the site, and the run must continue. Conflating it with
// a broken worker would abort a 600-URL sweep at the first dead host.
func TestRenderTreatsNavigationFailureAsASiteOutcome(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":false,"mode":"render","requested_url":"https://nope.invalid/","status":0,"error":"net::ERR_NAME_NOT_RESOLVED","error_kind":"navigation"}'
`)

	res, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://nope.invalid/"})
	if err != nil {
		t.Fatalf("Render: %v, want nil - an unreachable site is an answer", err)
	}
	if res.OK {
		t.Error("OK = true, want false")
	}
	if res.ErrorKind != KindNavigation {
		t.Errorf("ErrorKind = %q, want %q", res.ErrorKind, KindNavigation)
	}
	if res.Error != "net::ERR_NAME_NOT_RESOLVED" {
		t.Errorf("Error = %q", res.Error)
	}
}

func TestRenderTreatsAProcessTimeoutAsASiteLevelTimeout(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
sleep 5
`)
	worker.Timeout = 150 * time.Millisecond

	started := time.Now()
	res, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://slow.example/"})
	if err != nil {
		t.Fatalf("Render: %v, want a timeout Result rather than an error", err)
	}
	if res.OK || res.ErrorKind != KindTimeout {
		t.Errorf("res = %+v, want OK=false with kind timeout", res)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Render took %s; the worker budget was not enforced", elapsed)
	}
}

func TestRenderTreatsEscalationFailureAsASiteOutcome(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":false,"mode":"agent","requested_url":"https://example.com/careers","status":0,"error":"the model is unreachable","error_kind":"agent"}'
`)

	res, err := worker.Render(context.Background(), Request{Mode: ModeAgent, URL: "https://example.com/careers"})
	if err != nil {
		t.Fatalf("Render: %v, want nil - a failed escalation says nothing about the URL", err)
	}
	if res.OK || res.ErrorKind != KindAgent {
		t.Errorf("res = %+v, want OK=false with kind agent", res)
	}
}

// A broken environment is not a site verdict. Every URL would fail identically, so the run has to
// stop rather than write "unverifiable" 600 times.
func TestRenderPromotesConfigLaunchAndProtocolFailuresToRunLevelErrors(t *testing.T) {
	for _, kind := range []string{KindConfig, KindLaunch, KindProtocol} {
		t.Run(kind, func(t *testing.T) {
			worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":false,"mode":"render","requested_url":"https://example.com/","status":0,"error":"boom","error_kind":"`+kind+`"}'
`)
			_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
			var werr *Error
			if !errors.As(err, &werr) {
				t.Fatalf("err = %v, want a run-level error", err)
			}
			if werr.Kind != kind {
				t.Errorf("Kind = %q, want %q", werr.Kind, kind)
			}
		})
	}
}

// Contract drift must fail loudly. Treating an unknown kind as a site fact would write a future
// worker failure into the attempts table as a verdict about a company.
func TestRenderRejectsAnUnknownErrorKindAsProtocolDrift(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":false,"mode":"render","requested_url":"https://example.com/","status":0,"error":"?","error_kind":"something_new"}'
`)

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindProtocol {
		t.Fatalf("err = %v, want a protocol error", err)
	}
}

func TestRenderRejectsAFailureWithNoErrorKind(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":false,"mode":"render","requested_url":"https://example.com/","status":0,"error":"?"}'
`)

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindProtocol {
		t.Fatalf("err = %v, want a protocol error", err)
	}
}

// A render that reports success but names no page cannot be gated. Accepting it would let an empty
// response read as a rejected page.
func TestRenderRejectsASuccessfulRenderWithNoFinalURL(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":true,"mode":"render","status":200,"body":"<html></html>"}'
`)

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindProtocol {
		t.Fatalf("err = %v, want a protocol error", err)
	}
	if !strings.Contains(werr.Message, "final_url") {
		t.Errorf("message = %q, want it to name the missing field", werr.Message)
	}
}

// An empty Command is a caller bug, and it must be reported as one rather than executed as nothing.
func TestRenderRejectsAnEmptyCommand(t *testing.T) {
	worker := &SubprocessWorker{}
	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindConfig {
		t.Fatalf("err = %v, want a config error", err)
	}
}

// A response larger than the capture bound is a protocol failure, not a silently half-read page.
func TestRenderRejectsAStdoutCaptureThatHitItsBound(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
i=0
while [ $i -lt 200 ]; do
  printf 'xxxxxxxxxx'
  i=$((i+1))
done
`)
	worker.MaxStdoutBytes = 64

	_, err := worker.Render(context.Background(), Request{Mode: ModeRender, URL: "https://example.com/"})
	var werr *Error
	if !errors.As(err, &werr) || werr.Kind != KindProtocol {
		t.Fatalf("err = %v, want a protocol error", err)
	}
	if !strings.Contains(werr.Message, "capture bound") {
		t.Errorf("message = %q, want it to name the capture bound", werr.Message)
	}
}

func TestRenderRespectsAnAlreadyCancelledContext(t *testing.T) {
	worker, _ := scriptWorker(t, `
cat >/dev/null
printf '%s' '{"ok":true,"mode":"render","final_url":"https://example.com/","status":200}'
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := worker.Render(ctx, Request{Mode: ModeRender, URL: "https://example.com/"})
	if err == nil {
		t.Fatal("Render returned no error for an already-cancelled context")
	}
}
