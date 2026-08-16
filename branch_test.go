package main

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// TestBranchOverridesDetachedHead: on a detached HEAD, --branch supplies the
// branch name so givi can classify and compute the version.
func TestBranchOverridesDetachedHead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	h.detachHead()

	// Without --branch, a detached HEAD is still an error.
	if _, _, err := runCaptureAll(t, h, "--debug"); err == nil {
		t.Fatal("expected error on detached HEAD without --branch")
	} else if !strings.Contains(err.Error(), "detached") {
		t.Errorf("unexpected error: %v", err)
	}

	// With --branch main, givi computes main's version.
	out, err := runCapture(t, h, "--branch", "main")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--branch main on detached HEAD: out=%q err=%v", out, err)
	}
}

// TestBranchOverrideClassifiesAsDevelop: the override drives branch
// classification, so a develop override yields a develop-style prerelease.
func TestBranchOverrideClassifiesAsDevelop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.detachHead()

	out, err := runCapture(t, h, "--branch", "develop")
	if err != nil {
		t.Fatalf("--branch develop on detached HEAD: err=%v", err)
	}
	if !strings.Contains(out, "-alpha.") {
		t.Errorf("expected a develop prerelease, got %q", out)
	}
}

// TestBranchMatchesCheckedOut: on an attached HEAD, a matching --branch is
// accepted and behaves as if it were absent.
func TestBranchMatchesCheckedOut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	out, err := runCapture(t, h, "--branch", "main")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--branch main on checked-out main: out=%q err=%v", out, err)
	}
}

// TestBranchMismatchErrors: on an attached HEAD, a --branch that differs from
// the checked-out branch is an error (guards against pointing it at the wrong
// branch).
func TestBranchMismatchErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // on main

	_, _, err := runCaptureAll(t, h, "--branch", "develop")
	if err == nil {
		t.Fatal("expected error for --branch mismatch on attached HEAD")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestBranchInFormatVar: the resolved branch (from --branch on a detached HEAD)
// is what {{.Branch}} expands to.
func TestBranchInFormatVar(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop") // feature versions are computed relative to develop
	h.commit("d1")
	h.newBranch("feature/cool")
	h.commit("f1")
	h.detachHead()

	out, err := runCapture(t, h, "--branch", "feature/cool", "--format", "{{.Branch}}")
	if err != nil {
		t.Fatalf("--branch with --format {{.Branch}}: err=%v", err)
	}
	if out != "feature/cool" {
		t.Errorf("{{.Branch}} = %q, want %q", out, "feature/cool")
	}
}

// TestBranchWithTagMainOnDetachedHead: --branch main lets --tag-main work on a
// detached HEAD (the CI scenario: checkout leaves HEAD at a SHA).
func TestBranchWithTagMainOnDetachedHead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0
	h.detachHead()

	out, err := runCapture(t, h, "--tag-main", "--branch", "main")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--tag-main --branch main on detached HEAD: out=%q err=%v", out, err)
	}
	ref, err := h.g.r.Reference(plumbing.NewTagReferenceName("0.1.0"), false)
	if err != nil {
		t.Fatalf("expected tag 0.1.0 to exist: %v", err)
	}
	if ref.Hash() != head {
		t.Errorf("tag points at %s, want HEAD %s", ref.Hash(), head)
	}
}
