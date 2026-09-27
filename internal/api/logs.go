// logs.go: the endpoint that exposes the server's own runtime log to the UI, for debugging the
// trigger paths (launch failures, internal errors) without shelling into the container.
package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// serverLogTailBytes bounds the response: a long-running server accumulates a large log, and the UI
// only needs the recent tail.
const serverLogTailBytes = 256 * 1024

// ServerLogResponse is the GET /api/logs/server response: the tail of the server's own log.
type ServerLogResponse struct {
	Log string `json:"log"`
}

// handleGetServerLog serves GET /api/logs/server.
func handleGetServerLog(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dataDir, "logs", "server.log")
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, ServerLogResponse{Log: ""})
				return
			}
			writeInternalError(w, fmt.Errorf("open server log: %w", err))
			return
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil {
			writeInternalError(w, fmt.Errorf("stat server log: %w", err))
			return
		}
		offset := int64(0)
		if info.Size() > serverLogTailBytes {
			offset = info.Size() - serverLogTailBytes
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			writeInternalError(w, fmt.Errorf("seek server log: %w", err))
			return
		}
		b, err := io.ReadAll(f)
		if err != nil {
			writeInternalError(w, fmt.Errorf("read server log: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, ServerLogResponse{Log: string(b)})
	}
}
