// Package exitcode documents the process exit codes that two separate binaries (cmd/scrape and
// cmd/batch) must agree on. They live in their own package rather than being duplicated so the
// contract cannot drift between the command that returns a code and the runner that interprets it.
package exitcode

// OllamaUnreachable is returned by cmd/scrape when the model host could not be reached after the
// configured retries. cmd/batch treats it as a stop-the-whole-sweep signal rather than one more
// per-company failure: when Ollama is down, every remaining company would fail the same way, and
// churning through them wastes time and the model host's patience.
const OllamaUnreachable = 6
