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

// TestRemoteMainWhenNoLocalMain: the permanent branch exists only as a
// remote-tracking ref (refs/remotes/origin/main), not as a local head — the
// typical fresh-clone / CI checkout where only the checked-out branch has a
// local head. branchCommit must fall back to origin/main so the version can
// still be computed against it.
func TestRemoteMainWhenNoLocalMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	root := h.commit("root") // main = 0.1.0
	h.newBranch("develop")
	h.commit("d1")

	// Ground truth with a local main present.
	want := h.version()

	// Emulate a clone where main is only a remote-tracking ref: record
	// origin/main at the local main tip, then drop the local main head. HEAD is
	// on develop (a local branch), matching the fresh-clone layout.
	h.addRemote("origin")
	h.remoteRef("origin", "main", root)
	h.deleteBranch("main")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("develop with remote-only main: err=%v", err)
	}
	if out != want {
		t.Errorf("develop with remote-only main = %q, want %q (local-main value)", out, want)
	}
}

// TestMainMasterNameFirstPreference pins how mainBranch reconciles the "main" vs
// "master" name preference with local-vs-remote resolution: the NAME preference
// wins first (main beats master), and only then is each name resolved
// local-or-remote. So a remote-only "main" is chosen over a local "master",
// because "main" is tried first (including its remote-tracking fallback) before
// "master" is ever consulted.
func TestMainMasterNameFirstPreference(t *testing.T) {
	t.Parallel()

	// Local "main" only, "master" present only as a remote-tracking ref: the
	// local main is used and master is never consulted.
	t.Run("LocalMainRemoteMaster", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t) // permanent branch is local "main"
		root := h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("develop")
		h.addRemote("origin")
		h.remoteRef("origin", "master", root) // stray remote-only master

		got, name, err := resolveMain(t, h)
		if err != nil {
			t.Fatalf("mainBranch: %v", err)
		}
		if name != "main" {
			t.Errorf("picked %q, want %q (local main preferred by name)", name, "main")
		}
		local := branchTip(t, h, "main")
		if got != local {
			t.Errorf("main resolved to %s, want local main %s", short(got), short(local))
		}
	})

	// Local "master" only, "main" present only as a remote-tracking ref: the
	// NAME preference (main > master) wins, so the remote-only main is chosen
	// over the local master.
	t.Run("LocalMasterRemoteMain", func(t *testing.T) {
		t.Parallel()
		h := newHarnessNamed(t, "master") // permanent branch is local "master"
		root := h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("develop")
		h.addRemote("origin")
		h.remoteRef("origin", "main", root) // remote-only main

		got, name, err := resolveMain(t, h)
		if err != nil {
			t.Fatalf("mainBranch: %v", err)
		}
		if name != "main" {
			t.Errorf("picked %q, want %q (remote main preferred by name over local master)", name, "main")
		}
		remoteMain := remoteTip(t, h, "origin", "main")
		if got != remoteMain {
			t.Errorf("main resolved to %s, want remote main %s", short(got), short(remoteMain))
		}
	})
}

// resolveMain runs mainBranch after setting the preferred remote from HEAD,
// returning the resolved commit hash and its name ("main"/"master").
func resolveMain(t *testing.T, h *harness) (plumbing.Hash, string, error) {
	t.Helper()
	branch, err := h.g.headBranch()
	if err != nil {
		t.Fatalf("headBranch: %v", err)
	}
	h.g.setPreferredRemoteFor(branch)
	c, name, err := h.g.mainBranch()
	if err != nil {
		return plumbing.ZeroHash, "", err
	}
	return c.Hash, name, nil
}

// branchTip returns the tip hash of a local branch.
func branchTip(t *testing.T, h *harness, branch string) plumbing.Hash {
	t.Helper()
	ref, err := h.g.r.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		t.Fatalf("resolve local branch %q: %v", branch, err)
	}
	return ref.Hash()
}

// remoteTip returns the tip hash of a remote-tracking ref.
func remoteTip(t *testing.T, h *harness, remote, branch string) plumbing.Hash {
	t.Helper()
	ref, err := h.g.r.Reference(plumbing.NewRemoteReferenceName(remote, branch), true)
	if err != nil {
		t.Fatalf("resolve remote ref %s/%s: %v", remote, branch, err)
	}
	return ref.Hash()
}

// TestRemoteDevelopWhenNoLocalDevelop: a feature branch's integration branch
// (develop) exists only as origin/develop. The feature-branch calculation
// consults develop via branchCommit, which must fall back to the remote ref.
func TestRemoteDevelopWhenNoLocalDevelop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	developTip := h.commit("d1")
	h.newBranch("feature/cool")
	h.commit("f1")

	want := h.version()

	h.addRemote("origin")
	h.remoteRef("origin", "develop", developTip)
	h.checkout("feature/cool") // ensure HEAD is on feature before dropping develop
	h.deleteBranch("develop")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("feature with remote-only develop: err=%v", err)
	}
	if out != want {
		t.Errorf("feature with remote-only develop = %q, want %q (local-develop value)", out, want)
	}
}

// TestRemoteMainNonOriginRemote: when there is no local main and no
// origin/main, a single non-origin remote carrying main is used.
func TestRemoteMainNonOriginRemote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	root := h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	want := h.version()

	h.addRemote("upstream")
	h.remoteRef("upstream", "main", root)
	h.deleteBranch("main")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("develop with upstream-only main: err=%v", err)
	}
	if out != want {
		t.Errorf("develop with upstream-only main = %q, want %q", out, want)
	}
}

// TestRemoteMainOriginWinsOverOtherRemote: origin/main is used outright even
// when another remote also carries main and points elsewhere; origin is never
// treated as ambiguous.
func TestRemoteMainOriginWinsOverOtherRemote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	root := h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	want := h.version()

	// origin/main at the real main tip; a stale upstream/main at develop's tip.
	other := mustHead(t, h)
	h.addRemote("origin")
	h.addRemote("upstream")
	h.remoteRef("origin", "main", root)
	h.remoteRef("upstream", "main", other)
	h.deleteBranch("main")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("develop with origin+upstream main: err=%v", err)
	}
	if out != want {
		t.Errorf("origin/main should win: got %q, want %q", out, want)
	}
}

// TestRemoteMainAmbiguousAcrossRemotes: with no local main and no origin/main,
// but main present on two different remotes, the name is ambiguous and genver
// errors rather than silently picking one.
func TestRemoteMainAmbiguousAcrossRemotes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	root := h.commit("root")
	h.newBranch("develop")
	other := h.commit("d1")

	h.addRemote("upstream")
	h.addRemote("fork")
	h.remoteRef("upstream", "main", root)
	h.remoteRef("fork", "main", other)
	h.deleteBranch("main")

	_, err := runCapture(t, h)
	if err == nil {
		t.Fatal("expected an ambiguity error, got nil")
	}
	for _, want := range []string{"ambiguous", "fork/main", "upstream/main"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error %q missing %q", err.Error(), want)
		}
	}
}

// TestRemoteMainUsesBranchUpstream: the checked-out branch's configured upstream
// remote (branch.develop.remote) selects which remote's main is used, resolving
// what would otherwise be an ambiguous multi-remote match. This mirrors the
// arc-ui-v2 case: on develop tracking origin, with main only as
// refs/remotes/*/main, origin/main is chosen because develop tracks origin.
func TestRemoteMainUsesBranchUpstream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	root := h.commit("root") // main tip = 0.1.0
	h.newBranch("develop")
	other := h.commit("d1")
	want := h.version() // ground truth with a local main present

	// main lives on two remotes at different commits; without disambiguation
	// this is ambiguous. develop tracks "origin", so origin/main must win.
	h.addRemote("origin")
	h.addRemote("upstream")
	h.remoteRef("origin", "main", root)
	h.remoteRef("upstream", "main", other)
	h.setUpstream("develop", "origin")
	h.deleteBranch("main")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("develop tracking origin with multi-remote main: err=%v", err)
	}
	if out != want {
		t.Errorf("upstream-selected origin/main: got %q, want %q", out, want)
	}
}

// TestRemoteMainUpstreamOverridesOrigin: the branch's configured upstream is
// consulted before "origin", so a non-origin upstream wins even when origin also
// carries the branch.
func TestRemoteMainUpstreamOverridesOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	root := h.commit("root") // real main tip = 0.1.0
	h.newBranch("develop")
	stale := h.commit("d1")
	want := h.version()

	// upstream/main at the real tip; a stale origin/main at develop's tip.
	// develop tracks "upstream", so upstream/main must win over origin/main.
	h.addRemote("origin")
	h.addRemote("upstream")
	h.remoteRef("upstream", "main", root)
	h.remoteRef("origin", "main", stale)
	h.setUpstream("develop", "upstream")
	h.deleteBranch("main")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("develop tracking upstream: err=%v", err)
	}
	if out != want {
		t.Errorf("upstream should win over origin: got %q, want %q", out, want)
	}
}

// TestRemoteVsLocalParity is the core guarantee for the remote-branch fallback:
// for a range of representative histories and checked-out branches, the version
// genver computes must be IDENTICAL whether the reference branches (main and
// develop) exist as local heads or only as remote-tracking refs.
//
// Each case builds the repo, snapshots the output with all branches local
// (the ground truth), then converts every reference branch that is not the
// checked-out branch into an origin-only tracking ref and asserts the output is
// byte-for-byte unchanged. develop is set to track origin so origin is the
// preferred remote, mirroring a normal clone.
func TestRemoteVsLocalParity(t *testing.T) {
	t.Parallel()

	// build sets up the scenario and returns the name of the branch that should
	// remain checked out (HEAD), which therefore stays local.
	cases := []struct {
		name  string
		build func(h *harness) (headBranch string)
	}{
		{
			// On develop after a couple of commits; main is the only other
			// reference branch. develop is HEAD, main becomes remote-only.
			name: "DevelopSimple",
			build: func(h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.commits(2)
				return "develop"
			},
		},
		{
			// On develop after releases and tags on main: exercises release
			// boundaries and counter math, with main resolved remotely.
			name: "DevelopAfterReleases",
			build: func(h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.commits(2)
				h.release("1.0.0")
				h.commits(3)
				h.release("1.1.0")
				h.commits(2)
				return "develop"
			},
		},
		{
			// On a feature branch: both main AND develop are reference branches
			// consulted during the calculation, and both become remote-only.
			name: "FeatureBranch",
			build: func(h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.commits(2)
				h.newBranch("feature/cool")
				h.commits(3)
				return "feature/cool"
			},
		},
		{
			// Feature branch with a feature merge already integrated into
			// develop (feature-minor bump), then more feature work.
			name: "FeatureAfterMergedFeature",
			build: func(h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.commit("d1")
				h.newBranch("feature/done")
				h.commit("fd1")
				h.checkout("develop")
				h.merge("feature/done")
				h.deleteBranch("feature/done")
				h.newBranch("feature/next")
				h.commits(2)
				return "feature/next"
			},
		},
		{
			// A bugfix branch off develop.
			name: "BugfixBranch",
			build: func(h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.commit("d1")
				h.release("1.0.0")
				h.commit("d2")
				h.newBranch("bugfix/x")
				h.commits(2)
				return "bugfix/x"
			},
		},
	}

	// The reference branches that, when present, get moved to remote-only
	// (unless they are the checked-out branch).
	refBranches := []string{"main", "develop"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Ground truth: everything local.
			hLocal := newHarness(t)
			head := tc.build(hLocal)
			hLocal.checkout(head)
			want, err := runCapture(t, hLocal)
			if err != nil {
				t.Fatalf("local scenario: err=%v", err)
			}

			// Rebuild an identical repo, then move every reference branch that
			// is not HEAD onto origin only.
			hRemote := newHarness(t)
			if got := tc.build(hRemote); got != head {
				t.Fatalf("scenario build is non-deterministic: head %q vs %q", got, head)
			}
			hRemote.checkout(head)
			hRemote.addRemote("origin")
			// develop tracks origin so origin is the preferred remote (the
			// normal clone layout), whether or not develop is HEAD.
			hRemote.setUpstream("develop", "origin")
			for _, br := range refBranches {
				if br == head {
					continue // the checked-out branch keeps its local head
				}
				if _, err := hRemote.g.r.Reference(plumbing.NewBranchReferenceName(br), true); err != nil {
					continue // branch not present in this scenario
				}
				hRemote.moveToRemote(br, "origin")
			}

			got, err := runCapture(t, hRemote)
			if err != nil {
				t.Fatalf("remote-only scenario: err=%v", err)
			}
			if got != want {
				t.Errorf("remote-only output = %q, want %q (local output); "+
					"version must not change when a reference branch is remote-only", got, want)
			}
			if want == "" {
				t.Fatal("ground-truth output was empty; scenario produced no version")
			}
		})
	}
}

// TestLocalRefBranchWinsOverDivergentRemote pins the resolution precedence when
// a local reference branch and its remote-tracking ref point at DIFFERENT
// commits: the local head always wins, and the remote ref is not consulted at
// all. The remote-tracking ref is only a fallback for when no local head exists,
// so a divergent origin/main must never change the computed version while a
// local main is present.
//
// The topology is chosen so the divergence is genuinely observable: main carries
// two untagged release commits (m1, m2) and develop is cut from m2. Untagged
// release boundaries are discovered by walking main's first-parent chain, so
// which commit main resolves to changes the boundary develop builds on:
//
//   - local main = m2 (ahead): m2 is a release boundary  -> develop = 0.1.3-alpha.1
//   - origin/main = m1 (behind): m2 is just a develop commit -> develop = 0.1.2-alpha.2
//
// The test asserts (a) the two paths really differ (non-vacuous) and (b) with a
// local main present, the divergent origin/main is ignored and the local value
// is produced.
func TestLocalRefBranchWinsOverDivergentRemote(t *testing.T) {
	t.Parallel()

	// build returns a harness on develop with local main at m2 and origin/main
	// deliberately behind at m1. develop tracks origin, so origin is the
	// preferred remote for the fallback.
	build := func(t *testing.T) *harness {
		h := newHarness(t)
		h.commit("root")
		h.commit("m1") // untagged release on main
		h.commit("m2") // untagged release on main
		h.newBranch("develop")
		h.commit("d1")

		// origin/main points at m1 (one commit behind local main = m2).
		h.addRemote("origin")
		h.setUpstream("develop", "origin")
		h.remoteRef("origin", "main", m1Hash(t, h))
		return h
	}

	// Ground truth with the local main present (m2).
	hLocal := build(t)
	wantLocal := hLocal.version()
	if wantLocal == "" {
		t.Fatal("local scenario produced no version")
	}

	// Non-vacuity check: drop local main so it resolves via origin/main (m1),
	// which must yield a DIFFERENT version.
	hRemote := build(t)
	hRemote.deleteBranch("main")
	remoteOnly, err := runCapture(t, hRemote)
	if err != nil {
		t.Fatalf("remote-only scenario: err=%v", err)
	}
	if remoteOnly == wantLocal {
		t.Fatalf("test is vacuous: remote-only (%q) equals local (%q); "+
			"the two mains must diverge for this test to be meaningful", remoteOnly, wantLocal)
	}

	// The real assertion: with the local main present, the divergent
	// origin/main is ignored and the local value is produced.
	got, err := runCapture(t, hLocal)
	if err != nil {
		t.Fatalf("divergent remote with local main present: err=%v", err)
	}
	if got != wantLocal {
		t.Errorf("got %q, want %q: local main must win over a divergent origin/main (which would give %q)",
			got, wantLocal, remoteOnly)
	}
}

// m1Hash returns the hash of main's first untagged release commit ("m1"): the
// commit one step below main's tip on the first-parent chain. Used to point
// origin/main one commit behind local main.
func m1Hash(t *testing.T, h *harness) plumbing.Hash {
	t.Helper()
	return parentTipHash(t, h, "main")
}

// parentTipHash returns the hash of the first-parent of the named local
// branch's tip, i.e. the commit one step behind that branch. Used to point a
// remote-tracking ref one commit behind its local counterpart.
func parentTipHash(t *testing.T, h *harness, branch string) plumbing.Hash {
	t.Helper()
	ref, err := h.g.r.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		t.Fatalf("resolve %s: %v", branch, err)
	}
	tip, err := h.g.r.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("%s tip: %v", branch, err)
	}
	parent, err := tip.Parent(0)
	if err != nil {
		t.Fatalf("%s tip parent: %v", branch, err)
	}
	return parent.Hash
}

// TestLocalDevelopWinsOverDivergentRemote is the develop analogue of
// TestLocalRefBranchWinsOverDivergentRemote: when a feature branch's integration
// branch (develop) exists both as a local head and a divergent origin/develop,
// the local develop head wins and origin/develop is ignored.
//
// The divergence is observable because it lies within the feature branch's
// ancestry: develop advances d1 -> d2 and feature/x is cut from d2. forkBase
// walks develop's first-parent chain, so which commit develop resolves to
// changes the feature's fork point (and thus its counter):
//
//   - local develop = d2 (ahead): fork point d2       -> feature = 0.2.0-x.1
//   - origin/develop = d1 (behind): d2 counts as feature work -> feature = 0.2.0-x.2
func TestLocalDevelopWinsOverDivergentRemote(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T) *harness {
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.commit("d2")
		h.newBranch("feature/x")
		h.commit("f1")

		// origin/develop points at d1 (one commit behind local develop = d2).
		h.addRemote("origin")
		h.setUpstream("feature/x", "origin")
		h.remoteRef("origin", "develop", parentTipHash(t, h, "develop"))
		h.checkout("feature/x")
		return h
	}

	// Ground truth with the local develop present (d2).
	hLocal := build(t)
	wantLocal := hLocal.version()
	if wantLocal == "" {
		t.Fatal("local scenario produced no version")
	}

	// Non-vacuity check: drop local develop so it resolves via origin/develop
	// (d1), which must yield a DIFFERENT version.
	hRemote := build(t)
	hRemote.deleteBranch("develop")
	remoteOnly, err := runCapture(t, hRemote)
	if err != nil {
		t.Fatalf("remote-only scenario: err=%v", err)
	}
	if remoteOnly == wantLocal {
		t.Fatalf("test is vacuous: remote-only (%q) equals local (%q); "+
			"the two develops must diverge for this test to be meaningful", remoteOnly, wantLocal)
	}

	// The real assertion: with the local develop present, the divergent
	// origin/develop is ignored and the local value is produced.
	got, err := runCapture(t, hLocal)
	if err != nil {
		t.Fatalf("divergent remote with local develop present: err=%v", err)
	}
	if got != wantLocal {
		t.Errorf("got %q, want %q: local develop must win over a divergent origin/develop (which would give %q)",
			got, wantLocal, remoteOnly)
	}
}

// TestRemoteAheadWinsOverLocal: when both a local and a remote reference branch
// exist, the remote wins if the local head is not ahead of it (local is an
// ancestor of remote) and they share a merge base. Here local develop is BEHIND
// origin/develop (a common fresh-clone state where local tracking refs lag the
// fetched remote): origin/develop = d2, local develop = d1, feature cut from d2.
// The remote (d2) must be adopted, so the feature's fork point is d2.
//
//   - remote develop = d2 (adopted): fork point d2       -> feature = 0.2.0-x.1
//   - local  develop = d1 (behind, NOT used): d2 counts   -> feature = 0.2.0-x.2
func TestRemoteAheadWinsOverLocal(t *testing.T) {
	t.Parallel()

	// buildRemoteOnly returns a repo where develop exists ONLY as origin/develop
	// at d2, giving the ground-truth "remote wins" version.
	buildRemoteOnly := func(t *testing.T) *harness {
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.commit("d2")
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("feature/x")
		h.addRemote("origin")
		h.setUpstream("feature/x", "origin")
		h.moveToRemote("develop", "origin") // origin/develop = d2, no local develop
		return h
	}

	// Ground truth: remote-only develop at d2.
	hWant := buildRemoteOnly(t)
	want := hWant.version()
	if want == "" {
		t.Fatal("remote-only scenario produced no version")
	}

	// The scenario under test: a local develop that lags at d1, plus
	// origin/develop at d2. The remote must win.
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	d1 := h.commit("d1")
	d2 := h.commit("d2")
	h.newBranch("feature/x")
	h.commit("f1")
	h.checkout("feature/x")
	h.addRemote("origin")
	h.setUpstream("feature/x", "origin")
	h.remoteRef("origin", "develop", d2) // remote ahead at d2
	h.setLocalBranch("develop", d1)      // local behind at d1

	// Sanity: confirm the "local wins" alternative would differ, so the test is
	// not vacuous. Point local develop at d2 as well momentarily is unnecessary;
	// instead assert the value we get equals the remote-only (d2) truth.
	got, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("remote-ahead scenario: err=%v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q: remote-ahead develop (d2) must win over behind local develop (d1)",
			got, want)
	}
	// Guard against a vacuous test: a purely local develop at d1 must give a
	// different version, proving the remote actually changed the outcome.
	hLocalOnly := newHarness(t)
	hLocalOnly.commit("root")
	hLocalOnly.newBranch("develop")
	hLocalOnly.commit("d1")
	d2b := hLocalOnly.commit("d2")
	hLocalOnly.newBranch("feature/x")
	hLocalOnly.commit("f1")
	hLocalOnly.checkout("feature/x")
	hLocalOnly.setLocalBranch("develop", parentHashOf(t, hLocalOnly, d2b)) // develop at d1
	localOnly, err := runCapture(t, hLocalOnly)
	if err != nil {
		t.Fatalf("local-only scenario: err=%v", err)
	}
	if localOnly == want {
		t.Fatalf("test is vacuous: local-only develop-at-d1 (%q) equals remote-at-d2 (%q)", localOnly, want)
	}
}

// parentHashOf returns the first-parent hash of the given commit.
func parentHashOf(t *testing.T, h *harness, hash plumbing.Hash) plumbing.Hash {
	t.Helper()
	c, err := h.g.r.CommitObject(hash)
	if err != nil {
		t.Fatalf("commit %s: %v", hash, err)
	}
	p, err := c.Parent(0)
	if err != nil {
		t.Fatalf("parent of %s: %v", hash, err)
	}
	return p.Hash
}

// TestRemoteDivergedKeepsLocal: when local and remote have DIVERGED (each has
// commits the other lacks), the local head wins, because local has commits not
// present in remote. origin/develop and local develop share a base but neither
// is an ancestor of the other.
func TestRemoteDivergedKeepsLocal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	base := h.commit("d1") // common base
	// Local develop advances to d2-local.
	localTip := h.commit("d2-local")
	h.newBranch("feature/x")
	h.commit("f1")
	h.checkout("feature/x")

	// Build a divergent remote commit off the common base (d1): a sibling of
	// d2-local that local does not contain.
	h.checkout("develop")
	h.setLocalBranch("develop", base) // temporarily move develop to base
	h.checkout("develop")
	remoteTip := h.commit("d2-remote")
	// Restore local develop to its real (divergent) tip.
	h.setLocalBranch("develop", localTip)
	h.checkout("feature/x")

	// The version with local develop present (at localTip).
	want := h.version()

	h.addRemote("origin")
	h.setUpstream("feature/x", "origin")
	h.remoteRef("origin", "develop", remoteTip) // diverged from local

	got, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("diverged scenario: err=%v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q: diverged remote must not override local develop", got, want)
	}
}

// TestRemoteUnrelatedHistoryKeepsLocal: when local and remote reference branches
// share NO common merge base (unrelated histories), the local head wins.
func TestRemoteUnrelatedHistoryKeepsLocal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.newBranch("feature/x")
	h.commit("f1")
	h.checkout("feature/x")

	want := h.version()

	// An orphan commit line with an independent root: unrelated to local
	// develop, so there is no merge base.
	h.orphanBranch("unrelated")
	orphanTip := h.commit("u1")
	h.checkout("feature/x")

	h.addRemote("origin")
	h.setUpstream("feature/x", "origin")
	h.remoteRef("origin", "develop", orphanTip)
	h.deleteBranch("unrelated")

	got, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("unrelated-history scenario: err=%v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q: unrelated remote develop must not override local develop", got, want)
	}
}

// TestRemoteAheadWithTaggedCommitWins: the remote reference branch is ahead of
// local and one of the remote-only commits (absent from the local head) carries
// a RELEASE TAG. Adopting the remote must bring that tagged commit in as the
// fork point / release boundary.
//
// develop advances root -> d1 -> d2, with d2 tagged 1.0.0; feature/x is cut from
// d2 (so the tag is in the feature's ancestry). local develop lags at d1 (the
// tagged d2 is not on the local develop line); origin/develop is at d2. Because
// the remote wins, the fork point is the tagged d2:
//
//   - remote develop = d2 (tagged, adopted): fork point d2 -> feature = 1.1.0-x.1
//   - local  develop = d1 (behind, NOT used): d2 counts     -> feature = 1.1.0-x.2
//
// (The 1.1.0 core comes from the globally-discovered 1.0.0 tag plus the feature
// bump; the COUNTER is what distinguishes remote-wins from local-wins, proving
// the tagged remote-only commit was adopted as the boundary.)
func TestRemoteAheadWithTaggedCommitWins(t *testing.T) {
	t.Parallel()

	// build creates the scenario; when pinLocalBehind is true it places local
	// develop at d1 (behind the tagged d2) with origin/develop at d2. When false
	// it leaves develop fully local at d2 (the ground-truth remote-wins value,
	// since d2 is what the remote holds).
	build := func(t *testing.T, pinLocalBehind bool) *harness {
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		d1 := h.commit("d1")
		d2 := h.commit("d2")
		h.tag("1.0.0", d2)
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("feature/x")
		h.addRemote("origin")
		h.setUpstream("feature/x", "origin")
		if pinLocalBehind {
			h.remoteRef("origin", "develop", d2) // remote ahead, holds tagged d2
			h.setLocalBranch("develop", d1)      // local behind at d1
		}
		return h
	}

	// Ground truth: develop fully local at d2 (== what the remote holds).
	hWant := build(t, false)
	want := hWant.version()
	if want == "" {
		t.Fatal("ground-truth scenario produced no version")
	}

	// Non-vacuity: local develop pinned at d1 with NO remote must differ.
	hLocalOnly := build(t, false)
	hLocalOnly.setLocalBranch("develop", parentTipHash(t, hLocalOnly, "develop"))
	localOnly, err := runCapture(t, hLocalOnly)
	if err != nil {
		t.Fatalf("local-only scenario: err=%v", err)
	}
	if localOnly == want {
		t.Fatalf("test is vacuous: local-only develop-at-d1 (%q) equals remote-at-d2 (%q)", localOnly, want)
	}

	// The real assertion: remote ahead with the tagged d2 must win over the
	// behind local develop.
	h := build(t, true)
	got, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("remote-ahead-tagged scenario: err=%v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q: remote develop holding the tagged commit must win (local-only would give %q)",
			got, want, localOnly)
	}
}

// TestReferenceBranchResolutionDebugLogging asserts that --debug traces how each
// reference branch was resolved: which ref won (local vs remote-tracking) and
// the reason. This makes the reconciliation logic observable in the trace.
func TestReferenceBranchResolutionDebugLogging(t *testing.T) {
	t.Parallel()

	// Remote wins: local main behind origin/main (common base). HEAD on develop.
	t.Run("RemoteWins", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		m1 := h.commit("m1") // local main pinned here (behind)
		m2 := h.commit("m2") // origin/main tip (ahead)
		h.newBranch("develop")
		h.commit("d1")
		h.addRemote("origin")
		h.setUpstream("develop", "origin")
		h.remoteRef("origin", "main", m2) // remote ahead at m2
		h.setLocalBranch("main", m1)      // local behind at m1

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: err=%v", err)
		}
		if !strings.Contains(stderr, `resolve branch "main": remote`) ||
			!strings.Contains(stderr, "wins over local") ||
			!strings.Contains(stderr, "local is behind remote") {
			t.Errorf("expected a remote-wins trace for main; got:\n%s", stderr)
		}
	})

	// Local wins: local main ahead of origin/main. HEAD on develop.
	t.Run("LocalWins", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		localTip := h.commit("m2") // local main ahead at m2
		h.newBranch("develop")
		h.commit("d1")
		h.addRemote("origin")
		h.setUpstream("develop", "origin")
		h.remoteRef("origin", "main", parentHashOf(t, h, localTip)) // origin/main at m1 (behind)

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: err=%v", err)
		}
		if !strings.Contains(stderr, `resolve branch "main": local`) ||
			!strings.Contains(stderr, "wins over remote") ||
			!strings.Contains(stderr, "local is ahead or diverged") {
			t.Errorf("expected a local-wins trace for main; got:\n%s", stderr)
		}
	})

	// Remote-only fallback: no local main, resolved via origin/main.
	t.Run("RemoteOnlyFallback", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		root := h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.addRemote("origin")
		h.setUpstream("develop", "origin")
		h.remoteRef("origin", "main", root)
		h.deleteBranch("main")

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: err=%v", err)
		}
		if !strings.Contains(stderr, `remote resolve "main": matched origin/main (preferred upstream)`) {
			t.Errorf("expected a remote-only fallback trace for main; got:\n%s", stderr)
		}
	})

	// Develop remote wins: HEAD on a feature branch, local develop behind
	// origin/develop (common base). Exercises branchCommit("develop").
	t.Run("DevelopRemoteWins", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		d1 := h.commit("d1")
		d2 := h.commit("d2")
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("feature/x")
		h.addRemote("origin")
		h.setUpstream("feature/x", "origin")
		h.remoteRef("origin", "develop", d2) // remote ahead at d2
		h.setLocalBranch("develop", d1)      // local behind at d1

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: err=%v", err)
		}
		if !strings.Contains(stderr, `resolve branch "develop": remote`) ||
			!strings.Contains(stderr, "wins over local") ||
			!strings.Contains(stderr, "local is behind remote") {
			t.Errorf("expected a remote-wins trace for develop; got:\n%s", stderr)
		}
	})

	// Develop local wins: HEAD on a feature branch, local develop ahead of
	// origin/develop.
	t.Run("DevelopLocalWins", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		localTip := h.commit("d2") // local develop ahead at d2
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("feature/x")
		h.addRemote("origin")
		h.setUpstream("feature/x", "origin")
		h.remoteRef("origin", "develop", parentHashOf(t, h, localTip)) // origin/develop at d1 (behind)

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: err=%v", err)
		}
		if !strings.Contains(stderr, `resolve branch "develop": local`) ||
			!strings.Contains(stderr, "wins over remote") ||
			!strings.Contains(stderr, "local is ahead or diverged") {
			t.Errorf("expected a local-wins trace for develop; got:\n%s", stderr)
		}
	})

	// Develop remote-only fallback: HEAD on a feature branch, no local develop,
	// resolved via origin/develop.
	t.Run("DevelopRemoteOnlyFallback", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		developTip := h.commit("d1")
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("feature/x")
		h.addRemote("origin")
		h.setUpstream("feature/x", "origin")
		h.remoteRef("origin", "develop", developTip)
		h.deleteBranch("develop")

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: err=%v", err)
		}
		if !strings.Contains(stderr, `remote resolve "develop": matched origin/develop (preferred upstream)`) {
			t.Errorf("expected a remote-only fallback trace for develop; got:\n%s", stderr)
		}
	})
}
