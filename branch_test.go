package main

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// TestBranchOverridesDetachedHead: on a detached HEAD, --branch supplies the
// branch name so genver can classify and compute the version.
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

	// With --branch main, genver computes main's version.
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

// TestBranchOverrideComputesFeatureVersionOnDetachedHead: on a detached HEAD,
// --branch supplies the branch identity for a short-lived (feature) branch and
// the FULL version math (fork point, commit count, feature-minor) must be
// exactly what the same repo computes with the branch actually checked out.
// This is the core CI scenario: a checkout that leaves HEAD at a bare SHA, with
// the branch passed via --branch. It asserts the exact version, not just the
// label shape.
func TestBranchOverrideComputesFeatureVersionOnDetachedHead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.newBranch("feature/cool")
	h.commit("f1")
	h.commit("f2")

	// The value with the branch attached is the ground truth.
	want := h.version()

	// Detach HEAD (bare SHA) and recompute purely from --branch.
	h.detachHead()
	out, err := runCapture(t, h, "--branch", "feature/cool")
	if err != nil {
		t.Fatalf("--branch feature/cool on detached HEAD: err=%v", err)
	}
	if out != want {
		t.Errorf("detached --branch feature/cool = %q, want %q (attached value)", out, want)
	}
}

// TestBranchOverrideMasterOnDetachedHead: on a detached HEAD, --branch master
// classifies the version against the "master" permanent branch (the master
// analogue of TestBranchOverridesDetachedHead's --branch main case).
func TestBranchOverrideMasterOnDetachedHead(t *testing.T) {
	t.Parallel()
	h := newHarnessNamed(t, "master")
	h.commit("root") // master = 0.1.0
	h.detachHead()

	out, err := runCapture(t, h, "--branch", "master")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--branch master on detached HEAD: out=%q err=%v", out, err)
	}
}

// TestBranchEmptyOnDetachedHeadErrors: an explicit empty --branch "" is treated
// as no override, so a detached HEAD still errors (it cannot classify without a
// branch). This is the detached counterpart of TestBranchEmptyIsNoOverride.
func TestBranchEmptyOnDetachedHeadErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.detachHead()

	_, err := runCapture(t, h, "--branch", "")
	if err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf(`--branch "" on detached HEAD: got err %v, want it to mention "detached"`, err)
	}
}

// TestBranchInvalidNameFailsEarly: a --branch argument containing a character
// git forbids in a ref name (spaces, ~ ^ : ?, control chars, "..", a leading
// "-", etc.) is rejected up front with a clear error, rather than being carried
// into classification and version output. Valid names (including hierarchical
// feature branches) are still accepted. The check runs before HEAD state is
// considered, so it also fires on a detached HEAD.
func TestBranchInvalidNameFailsEarly(t *testing.T) {
	t.Parallel()

	invalid := []string{
		"bad name", // space
		"bad~name", // tilde
		"bad^name", // caret
		"bad:name", // colon
		"bad?name", // question mark
		"a..b",     // consecutive dots
		"-x",       // leading dash
		"feature/", // empty trailing component
		"x\ty",     // control character (tab)
	}
	for _, name := range invalid {
		h := newHarness(t)
		h.commit("root")
		_, err := runCapture(t, h, "--branch", name)
		if err == nil || !strings.Contains(err.Error(), "invalid --branch name") {
			t.Errorf("--branch %q: got err %v, want an \"invalid --branch name\" error", name, err)
		}
	}

	// The rejection happens before HEAD is examined, so an invalid name fails
	// early even on a detached HEAD (not the "detached" error).
	hd := newHarness(t)
	hd.commit("root")
	hd.detachHead()
	if _, err := runCapture(t, hd, "--branch", "bad name"); err == nil ||
		!strings.Contains(err.Error(), "invalid --branch name") {
		t.Errorf("detached invalid --branch: got err %v, want \"invalid --branch name\"", err)
	}

	// Valid names are still accepted.
	for _, name := range []string{"main", "master", "develop", "feature/cool", "bugfix/ABC-1_foo"} {
		h := newHarnessNamed(t, "main")
		h.commit("root")
		if name != "main" {
			h.newBranch(name) // create and check out so an attached match succeeds
			h.commit("w1")
		}
		if _, err := runCapture(t, h, "--branch", name); err != nil {
			t.Errorf("--branch %q (valid): unexpected err %v", name, err)
		}
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

// TestBranchMatchesCheckedOutMaster: on an attached HEAD checked out on master,
// a matching --branch master is accepted and behaves as if it were absent, just
// like --branch main on main.
func TestBranchMatchesCheckedOutMaster(t *testing.T) {
	t.Parallel()
	h := newHarnessNamed(t, "master")
	h.commit("root")

	out, err := runCapture(t, h, "--branch", "master")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--branch master on checked-out master: out=%q err=%v", out, err)
	}
}

// TestBranchEmptyIsNoOverride: an explicit empty --branch "" is treated as no
// override (indistinguishable from omitting the flag) on an attached HEAD.
func TestBranchEmptyIsNoOverride(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	out, err := runCapture(t, h, "--branch", "")
	if err != nil || out != "0.1.0" {
		t.Fatalf(`--branch "" on main: out=%q err=%v`, out, err)
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
