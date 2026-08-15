package main

import (
	"fmt"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// These tests reconstruct — entirely in the in-memory filesystem — two section
// topologies that stress givi at scale: a small section built from a bugfix
// branch plus a trailing commit, and a large section (dozens of commits) that
// contains a feature merge and a bugfix branch whose merge-base sits inside it.
//
// Only the parts that determine the answer are reconstructed: the latest release
// boundary (its core comes from the tag name, so a single release suffices) and
// the commit graph between that boundary and the branch tip. Older releases are
// irrelevant — the section scan always builds on the highest reachable boundary.

// TestScenarioBugfixSectionPatch covers a develop section made of a bugfix
// branch (three commits) merged back at the release boundary, followed by one
// direct develop commit. A bugfix merge implies only a patch bump (no +semver
// directive, not a feature branch), so the patch component increments once and
// the develop counter equals the whole section size: 3 + 1 merge + 1 = 5.
func TestScenarioBugfixSectionPatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("6.14.0") // boundary core = 6.14.0; back on develop at the boundary tip

	// Bugfix branch off the boundary, three commits, merged back into develop.
	const bug = "bugfix/branch-a"
	h.newBranch(bug)
	h.commit("b1")
	h.commit("b2")
	h.commit("b3")
	h.checkout("develop")
	h.mergePR(bug, 1, "acme-org") // bugfix PR merge -> patch, not minor
	h.deleteBranch(bug)

	// One trailing direct develop commit.
	h.commit("d1")

	// 3 bugfix commits + 1 merge + 1 trailing = 5 commits, all patch.
	h.want("6.14.1-alpha.5")
}

// TestScenarioNoDevelopBranchesOffMain covers the flow where there is no
// permanent "develop" branch and short-lived branches are cut directly from
// main, merged back, and deleted. Non-main branches are versioned relative to
// main: every main commit above the latest tag is itself a release boundary, so
// a branch with no commits of its own builds straight on main's release core
// (counter 0), a bugfix branch takes a patch increment, and a feature branch
// takes a minor increment immediately — mirroring the develop-based flow but
// with main as the integration branch.
func TestScenarioNoDevelopBranchesOffMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0") // main root
	h.tag("2.1.0", mustHead(t, h))
	h.want("2.1.0") // sanity: main reads the tag

	// Bugfix branch off main. With no develop branch, it is versioned relative
	// to main. No commits yet -> builds on the 2.1.0 release, counter 0.
	const bug = "bugfix/ABC-123-foo_bar"
	h.newBranch(bug)
	h.want("2.1.0-ABC-123-foo-bar.0")

	// One direct commit -> patch increment, counter 1.
	h.commit("b1")
	h.want("2.1.1-ABC-123-foo-bar.1")

	// Merge the bugfix branch back into main and delete it: a plain (non-feature)
	// merge bumps patch, so main becomes 2.1.1.
	h.checkout("main")
	h.mergePR(bug, 1, "acme-org")
	h.deleteBranch(bug)
	h.want("2.1.1")

	// Feature branch off main. A feature branch takes a minor increment
	// immediately, even with no commits of its own.
	const feat = "feature/cool-abc"
	h.newBranch(feat)
	h.want("2.2.0-cool-abc.0")

	// Two commits advance only the branch counter; the minor stays in scope.
	h.commit("f1")
	h.commit("f2")
	h.want("2.2.0-cool-abc.2")

	// Merge the feature branch into main: a feature merge bumps minor -> 2.2.0.
	h.checkout("main")
	h.mergePR(feat, 2, "acme-org")
	h.deleteBranch(feat)
	h.want("2.2.0")
}

// TestScenarioBugfixMergedThenCheckedOutAgain covers checking out a short-lived
// branch again AFTER it has been merged into develop but before it is deleted.
// Once merged, the branch tip is an ancestor of develop, so a plain merge-base
// with develop collapses onto the branch tip and would erase the branch's own
// increment (reporting .0). The version must instead stay stable: the branch is
// versioned against its fork point on develop's mainline, so its own commit
// still counts and the increment remains what it was before the merge (.1).
func TestScenarioBugfixMergedThenCheckedOutAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("0.39.5") // boundary core = 0.39.5; back on develop at the boundary tip

	// Bugfix branch off the boundary with a single commit.
	const bug = "bugfix/ABC-1234_fix_something"
	h.newBranch(bug)
	h.commit("b1")
	// Before the merge: one commit of its own -> patch increment, counter 1.
	h.want("0.39.6-ABC-1234-fix-something.1")

	// Merge the bugfix branch into develop, but do NOT delete it.
	h.checkout("develop")
	h.mergePR(bug, 175, "align-platform")

	// Check the branch out again. Its tip is now an ancestor of develop, but the
	// version must be unchanged: still counter 1, not 0.
	h.checkout(bug)
	h.want("0.39.6-ABC-1234-fix-something.1")
}

// TestScenarioBugfixMergedAfterDevelopAdvanced covers the same post-merge
// checkout, but with develop advancing (and other branches merging in) between
// the fork and the merge. The fork point sits below the newer develop commits,
// so the branch's own commit must still be the only thing counted (counter 1);
// the develop churn since the fork must not leak into the branch's increment.
func TestScenarioBugfixMergedAfterDevelopAdvanced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("1.2.0") // boundary core = 1.2.0

	// Bugfix branch off the boundary with a single commit.
	const bug = "bugfix/ABC-1000"
	h.newBranch(bug)
	h.commit("b1")

	// Meanwhile develop advances with its own direct commits.
	h.checkout("develop")
	h.commit("d1")
	h.commit("d2")

	// Now merge the (stale) bugfix branch into the advanced develop; keep it.
	h.mergePR(bug, 1, "acme-org")

	// Re-check out the branch: its fork point is below d1/d2, so only its own
	// commit counts -> counter 1. The core is the boundary + patch = 1.2.1.
	h.checkout(bug)
	h.want("1.2.1-ABC-1000.1")
}

// TestScenarioDevelopMergedIntoBugfixPatch covers merging develop back INTO a
// short-lived branch when develop has only accumulated patch-level commits. The
// merge advances the branch's fork point up develop's mainline to develop's tip,
// so the branch now builds on develop's (patch) section: the core stays at the
// same patch level it already had, and only the trailing counter advances (the
// branch's own commit plus the merge commit -> 2).
func TestScenarioDevelopMergedIntoBugfixPatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("0.39.5") // boundary core = 0.39.5

	// Bugfix branch off the boundary with a single commit -> patch, counter 1.
	const bug = "bugfix/ABC-1000"
	h.newBranch(bug)
	h.commit("b1")
	h.want("0.39.6-ABC-1000.1")

	// develop advances with plain (patch-only) direct commits.
	h.checkout("develop")
	h.commit("d1")
	h.commit("d2")

	// Merge develop into the bugfix branch. develop only had patch commits, so
	// the core stays 0.39.6; the counter bumps (b1 + merge commit -> 2).
	h.checkout(bug)
	h.merge("develop")
	h.want("0.39.6-ABC-1000.2")
}

// TestScenarioDevelopMergedIntoBugfixMinor covers merging develop back INTO a
// short-lived branch after develop accrued a minor-level bump (a feature merge).
// The develop section now carries a minor bump, and merging it into the branch
// raises the branch's effective bump from patch to minor: the core moves from
// the patch level (0.39.6) to the minor level (0.40.0).
func TestScenarioDevelopMergedIntoBugfixMinor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("0.39.5") // boundary core = 0.39.5

	// Bugfix branch off the boundary with a single commit -> patch, counter 1.
	const bug = "bugfix/ABC-1000"
	h.newBranch(bug)
	h.commit("b1")
	h.want("0.39.6-ABC-1000.1")

	// develop accrues a minor bump via a feature PR merge (feature commit + merge).
	h.checkout("develop")
	const feat = "feature/cool-xyz"
	h.newBranch(feat)
	h.commit("f1")
	h.checkout("develop")
	h.mergePR(feat, 1, "acme-org")
	h.deleteBranch(feat)

	// Merge develop into the bugfix branch. develop's minor bump wins: the core
	// goes patch -> minor (0.40.0). Counter counts the branch's own commit plus
	// the merge commit (2).
	h.checkout(bug)
	h.merge("develop")
	h.want("0.40.0-ABC-1000.2")
}

// TestScenarioDevelopMergedIntoBugfixMajor covers merging develop back INTO a
// short-lived branch after develop accrued a major-level bump (a "+semver:
// major" commit). Merging it into the branch raises the branch's effective bump
// all the way to major: the core moves to the next major (1.0.0).
func TestScenarioDevelopMergedIntoBugfixMajor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("0.39.5") // boundary core = 0.39.5

	// Bugfix branch off the boundary with a single commit -> patch, counter 1.
	const bug = "bugfix/ABC-1000"
	h.newBranch(bug)
	h.commit("b1")
	h.want("0.39.6-ABC-1000.1")

	// develop accrues a major bump via a direct "+semver: major" commit.
	h.checkout("develop")
	h.commit("rewrite everything\n\n+semver: major")

	// Merge develop into the bugfix branch. develop's major bump wins: the core
	// goes to the next major (1.0.0). Counter counts the branch's own commit plus
	// the merge commit (2).
	h.checkout(bug)
	h.merge("develop")
	h.want("1.0.0-ABC-1000.2")
}

// TestScenarioDevelopMergedIntoFeatureKeepsMinor covers merging develop back
// INTO a feature branch when develop only had patch commits. A feature branch's
// increment is floored at minor regardless, so the patch-only develop merge must
// not pull the core back down: it stays at the minor level (0.40.0) and only the
// counter advances.
func TestScenarioDevelopMergedIntoFeatureKeepsMinor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("0.39.5") // boundary core = 0.39.5

	// Feature branch off the boundary with a single commit -> minor, counter 1.
	const feat = "feature/cool-abc"
	h.newBranch(feat)
	h.commit("b1")
	h.want("0.40.0-cool-abc.1")

	// develop advances with plain (patch-only) direct commits.
	h.checkout("develop")
	h.commit("d1")
	h.commit("d2")

	// Merge develop into the feature branch. The feature floor keeps the core at
	// the minor level (0.40.0); only the counter advances (b1 + merge -> 2).
	h.checkout(feat)
	h.merge("develop")
	h.want("0.40.0-cool-abc.2")
}

// mustHead returns the current HEAD commit hash, failing the test on error.
func mustHead(t *testing.T, h *harness) plumbing.Hash {
	t.Helper()
	head, err := h.g.headCommit()
	if err != nil {
		t.Fatalf("headCommit: %v", err)
	}
	return head.Hash
}

// TestScenarioLargeSectionWithFeatureAndBugfix covers a large develop section
// (50 commits) that contains a feature merge — establishing a minor bump — and a
// bugfix branch whose merge-base with develop sits above that feature merge, so
// the minor stays in scope. develop reads a minor-bumped core with a counter of
// 50; the bugfix branch, with no commits of its own, reads counter 0; one commit
// on it advances only its own counter.
func TestScenarioLargeSectionWithFeatureAndBugfix(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("c0")
	h.newBranch("develop")
	h.commit("d0")
	h.release("0.56.0") // boundary core = 0.56.0; back on develop at the boundary tip

	// A feature PR merge establishes the section's minor bump (2 commits: the
	// feature commit plus the merge commit).
	const feat = "feature/branch-b"
	h.newBranch(feat)
	h.commit("f1")
	h.checkout("develop")
	h.mergePR(feat, 2, "acme-org") // feature PR merge -> minor
	h.deleteBranch(feat)

	// Advance develop to 40 commits into the section, then branch the bugfix.
	// Its merge-base therefore sits above the feature merge (minor stays in scope).
	for i := 1; i <= 38; i++ {
		h.commit(fmt.Sprintf("d%d", i))
	}
	const bug = "bugfix/branch-c"
	h.newBranch(bug) // no commits of its own yet
	h.checkout("develop")

	// Ten more develop commits bring the section to exactly 50.
	for i := 39; i <= 48; i++ {
		h.commit(fmt.Sprintf("d%d", i))
	}

	// develop: minor bump from the feature merge, 50 commits since the boundary.
	h.want("0.57.0-alpha.50")

	// bugfix branch: builds on the boundary with the section's minor already in
	// scope, no commits of its own -> counter 0.
	h.checkout(bug)
	h.want("0.57.0-branch-c.0")

	// One commit on the bugfix branch advances only its own counter.
	h.commit("b1")
	h.want("0.57.0-branch-c.1")
}
