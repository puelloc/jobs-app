// router.go: route wiring.
//
// Go 1.22+ ServeMux matches method and path together, so there is no router
// library here and no method switch inside the handlers. The one thing the
// standard mux does not do is answer errors in our JSON envelope - it writes
// plain-text 404 and 405 bodies - so the mux is wrapped to reshape exactly
// those two.
package api

import (
	"database/sql"
	"net/http"
	"strings"
)

// NewRouter returns the read-only HTTP handler for the job viewer API.
// Unknown paths answer 404 and a non-GET method on a known path answers 405,
// both in the envelope defined by types.go.
func NewRouter(db *sql.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/jobs", handleListJobs(db))
	mux.HandleFunc("GET /api/jobs/{id}", handleGetJob(db))

	// Deliberately no catch-all pattern: registering "/" would match every
	// method, and the mux would then serve it for POST /api/jobs instead of
	// reporting the method mismatch. Leaving the route table exact lets the mux
	// classify unknown paths (404) and wrong methods (405) itself.
	return jsonifyMuxErrors(mux)
}

// jsonifyMuxErrors replaces the mux's plain-text 404/405 bodies with the JSON
// error envelope. It distinguishes the mux's defaults from handler-written
// errors by Content-Type: a handler that calls writeError has already set
// application/json, so its body (and its specific message) is passed through
// untouched.
func jsonifyMuxErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&jsonErrorWriter{ResponseWriter: w}, r)
	})
}

type jsonErrorWriter struct {
	http.ResponseWriter
	rewritten bool
}

func (w *jsonErrorWriter) WriteHeader(status int) {
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			w.rewritten = true
			code, message := codeNotFound, "no such endpoint"
			if status == http.StatusMethodNotAllowed {
				code, message = codeMethodNotAllowed, "method not allowed"
			}
			// Write the envelope to the real writer. The mux still calls Write
			// with its plain-text body afterwards; that is swallowed below.
			writeError(w.ResponseWriter, status, code, message)
			return
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *jsonErrorWriter) Write(body []byte) (int, error) {
	if w.rewritten {
		// The JSON envelope is already written; drop the mux's plain-text body
		// while reporting a successful write so the mux does not log an error.
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}
