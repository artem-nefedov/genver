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
	// Assert the same answer via both the commit-graph and object-store paths.
	h.wantBothPaths("6.14.1-alpha.5")
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
	h.commit("c0")     // main root
	h.tag("2.1.0", mustHead(t, h))
	h.want("2.1.0") // sanity: main reads the tag

	// Bugfix branch off main. With no develop branch, it is versioned relative
	// to main. No commits yet -> builds on the 2.1.0 release, counter 0.
	const bug = "bugfix/ABC-123-foo_bar"
	h.newBranch(bug)
	h.wantBothPaths("2.1.0-ABC-123-foo-bar.0")

	// One direct commit -> patch increment, counter 1.
	h.commit("b1")
	h.wantBothPaths("2.1.1-ABC-123-foo-bar.1")

	// Merge the bugfix branch back into main and delete it: a plain (non-feature)
	// merge bumps patch, so main becomes 2.1.1.
	h.checkout("main")
	h.mergePR(bug, 1, "acme-org")
	h.deleteBranch(bug)
	h.wantBothPaths("2.1.1")

	// Feature branch off main. A feature branch takes a minor increment
	// immediately, even with no commits of its own.
	const feat = "feature/cool-abc"
	h.newBranch(feat)
	h.wantBothPaths("2.2.0-cool-abc.0")

	// Two commits advance only the branch counter; the minor stays in scope.
	h.commit("f1")
	h.commit("f2")
	h.wantBothPaths("2.2.0-cool-abc.2")

	// Merge the feature branch into main: a feature merge bumps minor -> 2.2.0.
	h.checkout("main")
	h.mergePR(feat, 2, "acme-org")
	h.deleteBranch(feat)
	h.wantBothPaths("2.2.0")
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
	// Assert via both the commit-graph and object-store paths.
	h.wantBothPaths("0.57.0-alpha.50")

	// bugfix branch: builds on the boundary with the section's minor already in
	// scope, no commits of its own -> counter 0.
	h.checkout(bug)
	h.wantBothPaths("0.57.0-branch-c.0")

	// One commit on the bugfix branch advances only its own counter.
	h.commit("b1")
	h.wantBothPaths("0.57.0-branch-c.1")
}
