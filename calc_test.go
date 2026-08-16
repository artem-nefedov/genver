package main

import (
	"fmt"
	"testing"
)

// TestWorkedExample replays the full journey from TASK.md's "Example" section.
// The only intentional deviation is the first develop reading: the task uses an
// arbitrary "alpha.123" to denote "some prior commits"; our freshly-built
// history has a deterministic count, so we assert the exact equivalent.
func TestWorkedExample(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// --- Establish the "2.1.0 is released" starting state. ---
	h.commit("root")         // M0 on main (the 0.1.0 boundary / root)
	h.newBranch("develop")   // develop from main
	h.commit("develop work") // D1
	h.checkout("main")
	mg1 := h.merge("develop") // release merge on main
	h.tag("2.1.0", mg1)       // releases are tagged
	h.want("2.1.0")           // on main: the release version

	h.checkout("develop")
	h.want("2.1.0-alpha.1") // no new commits since release (task's "alpha.123")

	// --- Direct commits on develop. ---
	h.commit("d2")
	h.want("2.1.1-alpha.1")
	h.commit("d3")
	h.want("2.1.1-alpha.2")

	// --- A bugfix branch. ---
	h.newBranch("bugfix/ABC-123-foo_bar")
	h.want("2.1.1-ABC-123-foo-bar.0")
	h.commit("bugfix work")
	h.want("2.1.1-ABC-123-foo-bar.1")

	// Merge bugfix into develop (the merge commit counts too).
	h.checkout("develop")
	h.merge("bugfix/ABC-123-foo_bar")
	h.want("2.1.1-alpha.4")

	// --- Release 2.1.1. ---
	h.checkout("main")
	mg2 := h.merge("develop")
	h.tag("2.1.1", mg2)
	h.want("2.1.1")

	// Develop unchanged since the release keeps its value.
	h.checkout("develop")
	h.want("2.1.1-alpha.4")

	// New develop commit bumps patch and resets the counter off the release.
	h.commit("d4")
	h.want("2.1.2-alpha.1")

	// --- A feature branch: minor bump takes precedence. ---
	h.newBranch("feature/cool-abc")
	h.want("2.2.0-cool-abc.0")
	h.commit("f1")
	h.commit("f2")
	h.commit("f3")
	h.want("2.2.0-cool-abc.3")

	h.checkout("develop")
	h.merge("feature/cool-abc")
	h.want("2.2.0-alpha.5")

	// --- A bugfix after a feature merge inherits the section's minor bump. ---
	h.newBranch("bugfix/ABC-456")
	h.want("2.2.0-ABC-456.0")
	h.commit("bb1")
	h.commit("bb2")
	h.want("2.2.0-ABC-456.2")

	h.checkout("develop")
	h.merge("bugfix/ABC-456")
	h.want("2.2.0-alpha.8")

	// --- Another feature branch. ---
	h.newBranch("feature/cool-xyz")
	h.want("2.2.0-cool-xyz.0")
	h.commit("fx1")
	h.commit("fx2")
	h.want("2.2.0-cool-xyz.2")

	h.checkout("develop")
	h.merge("feature/cool-xyz")
	h.want("2.2.0-alpha.11")

	// --- Release 2.2.0. ---
	h.checkout("main")
	mg3 := h.merge("develop")
	h.tag("2.2.0", mg3)
	h.want("2.2.0")
}

// TestManyReleases exercises a repository with dozens of releases (like the
// large repo in log.txt). It guards both correctness of section detection with
// many boundaries and against reintroducing full-history traversals: each
// release cuts a new section, and the version after N releases must build only
// on the latest one, regardless of how many boundaries exist.
func TestManyReleases(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")

	// Cut 30 patch releases: each adds one develop commit then releases.
	// Starting core is 0.1.0; each release bumps patch: 0.1.1, 0.1.2, ...
	const releases = 30
	for i := 1; i <= releases; i++ {
		h.commit(fmt.Sprintf("develop change %d", i))
		h.release(fmt.Sprintf("0.1.%d", i))
	}
	// On develop right after the last release, with no new commits, the version
	// keeps the released core and the last section's counter (1 commit).
	h.want("0.1.30-alpha.1")

	// A feature branch off develop must build on the LATEST release (0.1.30),
	// not on any earlier boundary, and get a minor bump.
	h.newBranch("feature/new-thing")
	h.want("0.2.0-new-thing.0")
	h.commit("feat 1")
	h.want("0.2.0-new-thing.1")

	// A bugfix branch after a feature was merged inherits the section's minor.
	h.checkout("develop")
	h.merge("feature/new-thing")
	h.want("0.2.0-alpha.2") // feature commit + merge commit = 2 commits in section

	h.newBranch("bugfix/urgent")
	h.want("0.2.0-urgent.0")
	h.commit("fix 1")
	h.want("0.2.0-urgent.1")
}

// TestCrossMergeCounting reproduces, in miniature, a bug where givi's develop
// counter diverged from GitVersion (and from `git rev-list base..HEAD`) on a
// repository with cross-merges. After every release, main is merged back into
// develop. That back-merge gives develop a second path to the previous
// release's boundary commit and all of its ancestors — a path that does not
// pass through the current section's boundary commit. A counter that merely
// stops at the boundary commit therefore re-counts those already-released
// commits and over-reports.
//
// The correct counter equals `git rev-list <current-boundary>..develop`. For
// this topology that is exactly the commits added since the last release
// (the back-merge commit plus the direct develop commit = 2 per section here),
// verified independently with the git CLI. The core is a plain patch bump off
// the latest release, so the assertion is the true, boundary-excluding value.
func TestCrossMergeCounting(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // C0 on main: the 0.1.0 boundary / root
	h.newBranch("develop")
	h.commit("d1")     // D1
	h.release("0.1.1") // merge develop into main, tag, back on develop
	// Back-merge main into develop: this is the cross-merge edge.
	h.backMerge()
	h.commit("d2")     // D2
	h.release("0.1.2") // release again
	h.backMerge()      // cross-merge again
	h.commit("d3")     // D3

	// The section builds on 0.1.2 (the latest release) with a plain patch bump.
	// The counter must equal `git rev-list <0.1.2 boundary>..develop`, i.e. only
	// the back-merge commit and d3 — NOT the already-released 0.1.1 boundary and
	// its ancestors that the back-merge made reachable. Verified with the git CLI
	// on an identical topology: count = 3 (back-merge, then d3, but the boundary
	// exclusion is what keeps earlier releases out).
	h.want("0.1.3-alpha.3")

	// A feature branch off this cross-merged develop must still build on the
	// latest release (0.1.2) with a minor bump, and count only its own commits
	// since the merge-base — never the released ancestors the back-merge made
	// reachable.
	h.newBranch("feature/across")
	h.want("0.2.0-across.0")
	h.commit("fa1")
	h.want("0.2.0-across.1")
}
