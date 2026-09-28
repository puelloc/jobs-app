package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestIsOllamaUnreachable(t *testing.T) {
	// A real child that exits 6 must be recognized; exit 1 and non-exit errors must not.
	if err := exec.Command("sh", "-c", "exit 6").Run(); !isOllamaUnreachable(err) {
		t.Errorf("exit 6 should be ollama-unreachable; err=%v", err)
	}
	if err := exec.Command("sh", "-c", "exit 1").Run(); isOllamaUnreachable(err) {
		t.Errorf("exit 1 should NOT be ollama-unreachable")
	}
	if isOllamaUnreachable(exec.Command("no-such-binary-xyz-123").Run()) {
		t.Errorf("a non-ExitError should NOT be ollama-unreachable")
	}
}

func TestResumePointWriteAndClear(t *testing.T) {
	dir := t.TempDir()
	writeResumePoint(dir, "abbott-laboratories")

	p := resumeFilePath(dir)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read resume point: %v", err)
	}
	if string(b) != "abbott-laboratories" {
		t.Errorf("resume point = %q, want abbott-laboratories", string(b))
	}

	clearResumePoint(dir)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("resume point should be removed; stat err=%v", err)
	}
}

func TestResumePointOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeResumePoint(dir, "first")
	writeResumePoint(dir, "second")

	b, err := os.ReadFile(resumeFilePath(dir))
	if err != nil {
		t.Fatalf("read resume point: %v", err)
	}
	if string(b) != "second" {
		t.Errorf("resume point = %q, want second (last write wins)", string(b))
	}
}
