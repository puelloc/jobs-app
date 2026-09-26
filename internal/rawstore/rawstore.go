// Package rawstore persists a source's payload to disk before anything parses it.
//
// The convention it implements is the one the existing scraper follows: the response body is
// written verbatim, before parsing, so a parse bug costs a re-parse of a stored payload rather than
// a re-fetch. The first run of a UTC day owns that day's file and a later run must not overwrite it.
//
// It is a package rather than a helper inside one command because every source needs the same
// behaviour and the failure modes are subtle: the O_EXCL reservation, the temp-file staging, and
// the cleanup on each error path.
package rawstore

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Write stages body in dataDir/raw and renames it into place.
//
// The filename is <source>-<YYYY-MM-DD>.<ext>, dated from startedAt in UTC.
//
// written reports whether this call created the file. It is false when a file for that source and
// date already exists, in which case path is the existing file and the caller decides what to do -
// process the run anyway, or treat the collision as a signal. A collision is not an error: the data
// in hand is still valid, and the on-disk capture for the day stays the earlier one.
func Write(dataDir, source, ext string, startedAt time.Time, body []byte) (path string, written bool, err error) {
	if source == "" {
		return "", false, fmt.Errorf("raw payload: source name is empty")
	}
	if ext == "" {
		return "", false, fmt.Errorf("raw payload: extension is empty")
	}

	rawDir := filepath.Join(dataDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return "", false, fmt.Errorf("create raw directory %s: %w", rawDir, err)
	}

	finalPath := filepath.Join(rawDir, Filename(source, ext, startedAt))

	// O_EXCL reserves the name, so the first run of a UTC day is authoritative and a second
	// cannot silently replace it.
	reservation, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// A collision is only a collision if what occupies the name is a file. A directory
			// (or anything else) would otherwise be reported as "already captured", and the run
			// would carry on believing a payload is on disk when it is not.
			info, statErr := os.Stat(finalPath)
			if statErr != nil {
				return "", false, fmt.Errorf("stat existing %s: %w", finalPath, statErr)
			}
			if !info.Mode().IsRegular() {
				return "", false, fmt.Errorf("raw payload path %s exists but is not a regular file (%s)", finalPath, info.Mode().Type())
			}
			return finalPath, false, nil
		}
		return "", false, fmt.Errorf("create %s: %w", finalPath, err)
	}
	if err := reservation.Close(); err != nil {
		return "", false, fmt.Errorf("close reservation %s: %w", finalPath, err)
	}

	// Stage in a temp file in the same directory so a crash mid-write cannot leave a truncated
	// file that looks like a valid capture, then rename into place.
	tmp, err := os.CreateTemp(rawDir, "."+source+"-*."+ext+".tmp")
	if err != nil {
		_ = os.Remove(finalPath)
		return "", false, fmt.Errorf("create temp file in %s: %w", rawDir, err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		_ = os.Remove(tmpName)
		_ = os.Remove(finalPath)
	}

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", false, fmt.Errorf("write payload: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", false, fmt.Errorf("sync payload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", false, fmt.Errorf("close temp payload: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		cleanup()
		return "", false, fmt.Errorf("rename payload into place: %w", err)
	}
	return finalPath, true, nil
}

// Filename is the documented capture name: source plus the run's UTC date.
//
//	<source>-YYYY-MM-DD.<ext>
func Filename(source, ext string, startedAt time.Time) string {
	return fmt.Sprintf("%s-%s.%s", source, startedAt.UTC().Format("2006-01-02"), ext)
}
