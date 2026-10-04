// jobs.go: the generic job-trigger endpoints. POST /api/pipeline/{name} launches one of the
// one-off pipeline jobs (sp1500, classify, batch, scraper) inside the server's own container, and
// GET /api/pipeline/{id}/log streams its stdout back so the UI can monitor a non-agent job the same
// way it watches an agent trace.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"jobsapp/internal/logging"
	"jobsapp/internal/store"
)

// jobSpec maps a trigger name to the binary it launches, the platform its scrape_runs row records,
// and the extra args the binary needs. The binary is resolved on PATH (in the image that is
// /usr/local/bin). `resolve` and `validate` are subcommands of the sp1500 binary.
type jobSpec struct {
	bin      string
	platform string
	args     []string
}

var triggerableJobs = map[string]jobSpec{
	"sp1500":   {bin: "sp1500", platform: "career_bootstrap"},
	"resolve":  {bin: "sp1500", platform: "career_resolution", args: []string{"resolve", "-progress"}},
	"validate": {bin: "sp1500", platform: "career_validation", args: []string{"validate", "-progress"}},
	"classify": {bin: "classify", platform: "career_classify", args: []string{"-commit"}},
	"batch":    {bin: "batch", platform: "career_batch"},
	"scraper":  {bin: "scraper", platform: "remoteok"},
}

// selfTrackingJobs are the commands that already create their own scrape_runs rows. The server must
// NOT create a run for them too: doing so showed two identical "career_resolution Running" rows for
// one click. Their output is teed into the server log instead of a per-run file.
var selfTrackingJobs = map[string]bool{
	"sp1500":   true, // indices -> wikipedia_sp500/400/600
	"resolve":  true, // -> career_resolution
	"validate": true, // -> career_validation
	"scraper":  true, // -> remoteok
}

// JobResponse is the POST /api/pipeline/{name} response. RunID is 0 for a self-tracking job, whose
// own run appears on the dashboard once the command starts.
type JobResponse struct {
	RunID int64 `json:"run_id"`
}

// handleTriggerJob serves POST /api/pipeline/{name}.
func handleTriggerJob(db *sql.DB, dataDir string, runner *jobRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		spec, ok := triggerableJobs[name]
		if !ok {
			writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("unknown job %q", name))
			return
		}

		// The batch sweep accepts options in its request body (which companies to skip, where to
		// resume, how many failures to tolerate). Parsed before the gate so a malformed body is a
		// 400 even while another job is running.
		jobArgs := append([]string{}, spec.args...)
		if name == "batch" {
			extra, err := batchArgsFromBody(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
				return
			}
			jobArgs = append(jobArgs, extra...)
		}

		if !runner.tryAcquire() {
			writeError(w, http.StatusConflict, codeConflict, "a job is already running; try again when it finishes")
			return
		}

		if selfTrackingJobs[name] {
			// The command records its own run(s); the server only launches it and captures its
			// output into the server log.
			logsDir := filepath.Join(dataDir, "logs")
			if err := os.MkdirAll(logsDir, 0o755); err != nil {
				runner.release()
				writeInternalError(w, fmt.Errorf("create logs dir: %w", err))
				return
			}
			logf, err := os.OpenFile(filepath.Join(logsDir, "server.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				runner.release()
				writeInternalError(w, fmt.Errorf("open server log: %w", err))
				return
			}
			cmd := exec.Command(spec.bin, jobArgs...)
			cmd.Stdout = logf
			cmd.Stderr = logf
			// The child inherits this request's trace, so everything the triggered job logs joins the
			// request that started it instead of arriving as an unrelated stream.
			cmd.Env = append(os.Environ(), logging.FromContext(r.Context()).Env()...)
			// Own process group so a stop signals the job plus its descendants.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				_ = logf.Close()
				runner.release()
				writeInternalError(w, fmt.Errorf("launch %s: %w", spec.bin, err))
				return
			}
			runner.register(cmd)
			go func() {
				_ = cmd.Wait()
				_ = logf.Close()
				runner.release()
			}()
			writeJSON(w, http.StatusAccepted, JobResponse{RunID: 0})
			return
		}

		platformID, err := store.PlatformIDByName(r.Context(), db, spec.platform)
		if err != nil {
			runner.release()
			writeInternalError(w, fmt.Errorf("%s platform: %w", spec.platform, err))
			return
		}
		runID, _, err := store.StartRun(r.Context(), db, platformID)
		if err != nil {
			runner.release()
			writeInternalError(w, fmt.Errorf("start run: %w", err))
			return
		}

		logPath := filepath.Join(dataDir, "jobs", fmt.Sprintf("%d.log", runID))
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			runner.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("log dir: %v", err)))
			writeInternalError(w, fmt.Errorf("create log dir: %w", err))
			return
		}
		logf, err := os.Create(logPath)
		if err != nil {
			runner.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("log file: %v", err)))
			writeInternalError(w, fmt.Errorf("create log file: %w", err))
			return
		}

		// The sweep's own run id goes with it so its structured logs can carry sweep_id and join this
		// row. Only batch takes the flag; classify creates no per-company work to attribute.
		if name == "batch" {
			jobArgs = append(jobArgs, "-run-id", strconv.FormatInt(runID, 10))
		}

		cmd := exec.Command(spec.bin, jobArgs...)
		cmd.Stdout = logf
		cmd.Stderr = logf
		cmd.Env = append(os.Environ(), logging.FromContext(r.Context()).Env()...)
		// Own process group so a stop signals the job plus its descendants.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			_ = logf.Close()
			runner.release()
			_ = store.FinishRun(r.Context(), db, runID, "error", 0, 0, 0, strPtr(fmt.Sprintf("launch: %v", err)))
			writeInternalError(w, fmt.Errorf("launch %s: %w", spec.bin, err))
			return
		}
		runner.register(cmd)

		// Reap the process, close the log, and finish the run in the background. When the run was
		// stopped, the stop handler already wrote the cancelled state, so skip FinishRun.
		go func() {
			waitErr := cmd.Wait()
			_ = logf.Close()
			if !runner.isStopped() {
				status := "ok"
				var errText *string
				if waitErr != nil {
					status = "error"
					errText = strPtr(fmt.Sprintf("%v", waitErr))
				}
				_ = store.FinishRun(context.Background(), db, runID, status, 0, 0, 0, errText)
			}
			runner.release()
		}()

		writeJSON(w, http.StatusAccepted, JobResponse{RunID: runID})
	}
}

// batchOptions is the request body for POST /api/pipeline/batch. Every field is optional: absent
// means the batch runner's default (no skip, full sweep, no resume point, no failure cap).
type batchOptions struct {
	SkipOK            bool   `json:"skip_ok"`
	SkipTraced        bool   `json:"skip_traced"`
	FromSlug          string `json:"from_slug"`
	StopAfterFailures int    `json:"stop_after_failures"`
	Limit             int    `json:"limit"`
}

// batchArgsFromBody decodes the batch request body into the flags cmd/batch understands. An empty
// or absent body (the existing UI's bare POST) decodes to zero options and no flags, preserving the
// current behavior.
func batchArgsFromBody(r *http.Request) ([]string, error) {
	var opts batchOptions
	if r.Body == nil || r.ContentLength == 0 {
		return nil, nil
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&opts); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("invalid batch options: %w", err)
	}

	var args []string
	if opts.SkipOK {
		args = append(args, "-skip-ok")
	}
	if opts.SkipTraced {
		args = append(args, "-skip-traced")
	}
	if opts.FromSlug != "" {
		args = append(args, "-from-slug", opts.FromSlug)
	}
	if opts.StopAfterFailures > 0 {
		args = append(args, "-stop-after-failures", strconv.Itoa(opts.StopAfterFailures))
	}
	if opts.Limit > 0 {
		args = append(args, "-limit", strconv.Itoa(opts.Limit))
	}
	return args, nil
}

// jobLogIDRE limits a log id to a single safe filename component.
var jobLogIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// strPtr returns a pointer to s, for the nullable error_text column.
func strPtr(s string) *string { return &s }

// JobLogResponse is the GET /api/jobs/{id}/log response: the command's stdout so far.
type JobLogResponse struct {
	ID      string `json:"id"`
	Present bool   `json:"present"`
	Log     string `json:"log"`
}

// handleGetJobLog serves GET /api/jobs/{id}/log.
func handleGetJobLog(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !jobLogIDRE.MatchString(id) {
			writeError(w, http.StatusBadRequest, codeBadRequest, "log id must be a safe filename")
			return
		}
		path := filepath.Join(dataDir, "jobs", id+".log")
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, JobLogResponse{ID: id, Present: false, Log: ""})
				return
			}
			writeInternalError(w, fmt.Errorf("read log %s: %w", id, err))
			return
		}
		writeJSON(w, http.StatusOK, JobLogResponse{ID: id, Present: true, Log: string(raw)})
	}
}
