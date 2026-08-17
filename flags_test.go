package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestFlagHelpAndVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	out, err := runCapture(t, h, "--help")
	if err != nil || !strings.Contains(out, "Usage: genver") {
		t.Errorf("--help: out=%q err=%v", out, err)
	}
	// --help beats --version.
	out, err = runCapture(t, h, "--help", "--version")
	if err != nil || !strings.Contains(out, "Usage: genver") {
		t.Errorf("--help priority: out=%q err=%v", out, err)
	}
	out, err = runCapture(t, h, "--version")
	if err != nil || out != version {
		t.Errorf("--version: out=%q err=%v", out, err)
	}
}

func TestDebugFlag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")

	// Without --debug, stderr stays empty and stdout has just the version.
	stdout, stderr, err := runCaptureAll(t, h)
	if err != nil {
		t.Fatalf("no flags: err=%v", err)
	}
	if stdout != "0.1.1-alpha.1" {
		t.Errorf("no flags stdout = %q, want 0.1.1-alpha.1", stdout)
	}
	if stderr != "" {
		t.Errorf("no flags should not write to stderr, got %q", stderr)
	}

	// With --debug, stdout is unchanged but stderr carries the trace.
	stdout, stderr, err = runCaptureAll(t, h, "--debug")
	if err != nil {
		t.Fatalf("--debug: err=%v", err)
	}
	if stdout != "0.1.1-alpha.1" {
		t.Errorf("--debug stdout = %q, want 0.1.1-alpha.1 (version must stay on stdout)", stdout)
	}
	for _, want := range []string{
		"branch classified as develop",
		"result: 0.1.1-alpha.1",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("--debug stderr missing %q; got:\n%s", want, stderr)
		}
	}
	// Every trace line must carry a nanosecond-precision timestamp.
	tsRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{9}: `)
	for line := range strings.SplitSeq(stderr, "\n") {
		if !tsRe.MatchString(line) {
			t.Errorf("trace line missing ms timestamp: %q", line)
		}
	}
}

func TestTagMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--tag-main: out=%q err=%v", out, err)
	}
	// The tag must now exist and point at HEAD.
	ref, err := h.g.r.Reference(plumbing.NewTagReferenceName("0.1.0"), false)
	if err != nil {
		t.Fatalf("expected tag 0.1.0 to exist: %v", err)
	}
	head, _ := h.g.headCommit()
	if ref.Hash() != head.Hash {
		t.Errorf("tag points at %s, want HEAD %s", ref.Hash(), head.Hash)
	}
	// Running again is a no-op success (tag already exists).
	if out, err := runCapture(t, h, "--tag-main"); err != nil || out != "0.1.0" {
		t.Errorf("second --tag-main: out=%q err=%v", out, err)
	}

	// On a non-main branch, --tag-main is ignored (no error, no tag).
	h.newBranch("develop")
	h.commit("d1")
	if _, err := runCapture(t, h, "--tag-main"); err != nil {
		t.Errorf("--tag-main on develop should be ignored, got err=%v", err)
	}
}

func TestTagMainDebugLogging(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	// First tag-main with --debug: the creation must be traced to stderr.
	_, stderr, err := runCaptureAll(t, h, "--tag-main", "--debug")
	if err != nil {
		t.Fatalf("--tag-main --debug: err=%v", err)
	}
	if !strings.Contains(stderr, `tag-main: created lightweight tag "0.1.0"`) {
		t.Errorf("expected tag creation trace, got:\n%s", stderr)
	}

	// Second run: tag already exists, which must also be traced.
	_, stderr, err = runCaptureAll(t, h, "--tag-main", "--debug")
	if err != nil {
		t.Fatalf("second --tag-main --debug: err=%v", err)
	}
	if !strings.Contains(stderr, `tag-main: tag "0.1.0" already exists`) {
		t.Errorf("expected already-exists trace, got:\n%s", stderr)
	}

	// On a non-main branch, --tag-main is ignored, and that is traced too.
	h.newBranch("develop")
	h.commit("d1")
	_, stderr, err = runCaptureAll(t, h, "--tag-main", "--debug")
	if err != nil {
		t.Fatalf("--tag-main --debug on develop: err=%v", err)
	}
	if !strings.Contains(stderr, `tag-main: ignored on non-main branch "develop"`) {
		t.Errorf("expected ignored-on-non-main trace, got:\n%s", stderr)
	}
}
