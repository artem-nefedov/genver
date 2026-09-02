package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestWorkflowWithCrossMerges builds a repository through the full workflow
// (releases, feature/bugfix branches, cross-merges) and checks the version on
// several branches. The cross-merges (main merged back into develop) stress the
// reachability walk, which must exclude already-released commits reached via a
// path that bypasses the current section's boundary commit.
func TestWorkflowWithCrossMerges(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.release("0.1.1")
	h.backMerge() // cross-merge to stress reachability
	h.commit("d2")
	h.release("0.1.2")
	h.backMerge()
	h.commit("d3")

	// Feature branch (minor bump), then merge it back.
	h.newBranch("feature/cool-abc")
	h.commit("f1")
	h.commit("f2")
	h.checkout("develop")
	h.merge("feature/cool-abc")

	// Bugfix branch (inherits the section's minor after the feature merge).
	h.newBranch("bugfix/ABC-1")
	h.commit("b1")
	h.want("0.2.0-ABC-1.1")

	h.checkout("develop")
	h.want("0.2.0-alpha.6")

	h.checkout("main")
	h.want("0.1.2")
}

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

// TestCrossMergeCounting reproduces, in miniature, a bug where genver's develop
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

// These tests reconstruct — entirely in the in-memory filesystem — two section
// topologies that stress genver at scale: a small section built from a bugfix
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

	// Advance develop to 40 commits into the section, then branch the bugfix.
	// Its merge-base therefore sits above the feature merge (minor stays in scope).
	h.commits(38)
	const bug = "bugfix/branch-c"
	h.newBranch(bug) // no commits of its own yet
	h.checkout("develop")

	// Ten more develop commits bring the section to exactly 50.
	h.commits(10)

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

func TestDeletingMergedBranchDoesNotChangeVersion(t *testing.T) {
	t.Parallel()

	// genver derives a merge's nature (feature/bugfix/directive) solely from the
	// merge commit's MESSAGE, never from branch refs — real git-flow deletes
	// short-lived branches after merge. Each case computes the version with the
	// merged branch ref still present, deletes it, and asserts the version is
	// unchanged.
	cases := []struct {
		name  string
		build func(t *testing.T, h *harness) string // returns branch name to delete
	}{
		{
			// A feature merge into develop earns a minor; the "feature/" nature
			// comes from the merge message, so deleting the branch is a no-op.
			name: "FeatureMergeIntoDevelop",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.newBranch("feature/x")
				h.commit("f1")
				h.checkout("develop")
				h.merge("feature/x")
				return "feature/x"
			},
		},
		{
			// A bugfix merge into develop earns only the patch floor.
			name: "BugfixMergeIntoDevelop",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.newBranch("bugfix/y")
				h.commit("b1")
				h.checkout("develop")
				h.merge("bugfix/y")
				return "bugfix/y"
			},
		},
		{
			// A direct feature merge into main.
			name: "FeatureMergeDirectToMain",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("feature/x")
				h.commit("f1")
				h.checkout("main")
				h.merge("feature/x")
				return "feature/x"
			},
		},
		{
			// A merge carrying an explicit "+semver:" directive: the directive
			// lives in the message, so deletion cannot affect it.
			name: "DirectiveMergeIntoMain",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("feature/x")
				h.commit("f1")
				h.checkout("main")
				h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: major")
				return "feature/x"
			},
		},
		{
			// A feature merge whose merged tip carries a reference tag: the tag is
			// keyed by commit hash, not by the branch ref, so deletion is a no-op.
			name: "FeatureMergeWithReferenceTag",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.newBranch("feature/x")
				h.commit("f1")
				h.tag("1.5.0-x.3", mustHead(t, h))
				h.checkout("develop")
				h.merge("feature/x")
				return "feature/x"
			},
		},
		{
			// The develop -> main release merge: main's release core is computed
			// from the merge commit's second parent (the develop tip reached via
			// the commit graph), not via the "develop" ref. Deleting develop after
			// the release must leave main's version unchanged. Note develop is a
			// long-lived branch, unlike the short-lived topic branches above.
			name: "DevelopReleaseMergeIntoMain",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.newBranch("feature/x")
				h.commit("f1")
				h.checkout("develop")
				h.merge("feature/x") // feature-minor inside the develop section
				h.checkout("main")
				h.merge("develop") // release merge develop -> main
				return "develop"
			},
		},
		{
			// A tagged release, then a second develop section and release: the
			// running release core walks main's first-parent chain and the develop
			// tips through the commit graph, so deleting develop is still a no-op.
			name: "DevelopReleaseMergeAfterTaggedRelease",
			build: func(t *testing.T, h *harness) string {
				h.commit("root")
				h.newBranch("develop")
				h.commit("d1")
				h.checkout("main")
				mg := h.merge("develop")
				h.tag("0.2.0", mg)
				h.checkout("develop")
				h.merge("main")
				h.commit("d2")
				h.checkout("main")
				h.merge("develop")
				return "develop"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			branch := tc.build(t, h)
			before := h.version()
			h.deleteBranch(branch)
			after := h.version()
			if before != after {
				t.Fatalf("deleting merged branch %q changed version: before %q, after %q", branch, before, after)
			}
		})
	}
}

// TestNoTags verifies the "root is 0.1.0" rule when the repo has no tags.
func TestNoTags(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.want("0.1.0-alpha.0") // no new develop commits, no tags -> root 0.1.0
	h.commit("d1")
	h.want("0.1.1-alpha.1")
	h.checkout("main")
	h.want("0.1.0") // main root treated as 0.1.0
	h.commit("m1")
	h.want("0.1.1") // direct commit on main bumps patch
}

// TestMasterBranch verifies that a repository whose permanent release branch is
// named "master" behaves identically to one named "main": master is classified
// as the release branch, develop and other branches are versioned relative to
// it, and a direct branch merge into master advances the release core.
func TestMasterBranch(t *testing.T) {
	t.Parallel()

	// The permanent-branch + develop + feature flow, but on "master".
	t.Run("ReleaseDevelopFeature", func(t *testing.T) {
		t.Parallel()
		h := newHarnessNamed(t, "master")
		h.commit("root") // master root treated as 0.1.0
		h.want("0.1.0")

		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.commit("d1")
		h.want("0.1.1-alpha.1")

		// Feature branch off develop -> minor increment, relative to master.
		h.newBranch("feature/x")
		h.want("0.2.0-x.0") // no commits of its own yet
		h.commit("f1")
		h.want("0.2.0-x.1")

		// Merge feature into develop, then release develop into master and tag.
		h.checkout("develop")
		h.merge("feature/x")
		h.want("0.2.0-alpha.3")
		h.checkout("master")
		mg := h.merge("develop")
		h.tag("0.2.0", mg)
		h.want("0.2.0") // master reflects the release
	})

	// Short-lived branch cut directly from master (no develop), and a direct
	// feature merge into master bumps minor once.
	t.Run("BranchesOffMaster", func(t *testing.T) {
		t.Parallel()
		h := newHarnessNamed(t, "master")
		root := h.commit("root")
		h.tag("2.1.0", root)
		h.want("2.1.0")

		// Bugfix branch off master, versioned relative to master.
		h.newBranch("bugfix/ABC-9")
		h.want("2.1.0-ABC-9.0")
		h.commit("b1")
		h.want("2.1.1-ABC-9.1")

		// Direct feature merge into master bumps minor, once.
		h.checkout("master")
		h.newBranch("feature/cool")
		h.commit("f1")
		h.want("2.2.0-cool.1")
		h.checkout("master")
		h.merge("feature/cool")
		h.want("2.2.0")
	})

	// Regression: a short-lived branch that forks directly FROM a
	// direct-feature-merge commit on the permanent branch (a no-develop
	// "mainline" flow). The merge commit's release core is already established
	// (0.2.0 here), so the branch must build straight on it — a non-feature
	// branch adds a patch, giving 0.2.1. Previously genver re-scanned the
	// section below the fork point, which still contained the feature merge, and
	// re-applied its minor bump on top of 0.2.0, wrongly yielding 0.3.0. Checked
	// for both permanent-branch names ("master" and "main").
	for _, tc := range []struct{ name, mainName string }{
		{"BranchForksFromDirectFeatureMergeMaster", "master"},
		{"BranchForksFromDirectFeatureMergeMain", "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarnessNamed(t, tc.mainName)
			h.commit("root") // root treated as 0.1.0

			// A direct feature merge bumps minor once: 0.1.0 -> 0.2.0.
			h.newBranch("feature/cool")
			h.commit("f1")
			h.checkout(tc.mainName)
			h.merge("feature/cool")
			h.want("0.2.0")

			// Fork a non-feature branch from that merge commit and add a commit.
			h.newBranch("misc/ABC-1")
			h.commit("m1")
			h.want("0.2.1-ABC-1.1") // builds on 0.2.0 + one patch, NOT 0.3.0
		})
	}
}

// TestBothMainAndMasterMergedIntoDevelop covers the unusual repository where
// BOTH permanent-branch names exist: "main" (the preferred release branch) and a
// separate "master" with its own divergent commit. Both are merged into develop
// and develop is then released into main. genver prefers "main" as the release
// branch, so develop is versioned relative to main; master is a plain release
// branch computed off its OWN first-parent chain. The key correctness points:
// merging master into develop counts master's unique commit exactly once (via
// the boundary-excluding section walk), master's own commits do not lift the
// core beyond the patch they imply, and the develop->main release publishes
// develop's accumulated core. master itself is untouched by the release.
func TestBothMainAndMasterMergedIntoDevelop(t *testing.T) {
	t.Parallel()
	h := newHarness(t) // permanent release branch "main"
	h.commit("root")   // main root / 0.1.0 boundary
	h.want("0.1.0")

	// A separate "master" branch forked at the root, with its own commit. Both
	// permanent-style branches now exist with divergent tips.
	h.newBranch("master")
	h.commit("m-master")
	h.want("0.1.1") // master is a release branch: root + one direct commit

	// main advances with its own direct commit (a boundary at 0.1.1).
	h.checkout("main")
	h.commit("m-main")
	h.want("0.1.1")

	// develop off main; one commit of its own.
	h.newBranch("develop")
	h.commit("d1")
	h.want("0.1.2-alpha.1") // builds on the m-main boundary (0.1.1), +1 patch

	// Merge BOTH permanent branches into develop. main is already reachable, so
	// it adds only the merge commit; master brings in its unique commit plus the
	// merge commit. master's commits are plain patch, so the core stays 0.1.2.
	h.merge("main")
	h.want("0.1.2-alpha.2") // + main-merge commit
	h.merge("master")
	h.want("0.1.2-alpha.4") // + m-master and the master-merge commit

	h.commit("d2")
	h.want("0.1.2-alpha.5")

	// Release develop into main: main publishes develop's accumulated core.
	h.checkout("main")
	h.merge("develop")
	h.want("0.1.2")

	// master is not on the develop->main path, so its own version is unchanged.
	h.checkout("master")
	h.want("0.1.1")
}

// TestMultipleMainCommits exercises topologies where main carries more than the
// single root commit: several direct commits, direct (hotfix-style) commits
// interleaved with release merges, and a tag placed mid-history on main. Each
// case asserts main's release core plus the derived develop / feature values, so
// the forward pass over main's first-parent chain is covered beyond the common
// single-root shape. Every direct commit on main is itself a release boundary at
// its core, so develop builds on the highest main core it can reach rather than
// falling back to the 0.1.0 root.
func TestMultipleMainCommits(t *testing.T) {
	t.Parallel()
	// Several direct commits on main, each a patch bump; develop branched from
	// the latest main commit builds on that main core (0.1.2), not the root.
	t.Run("DirectCommitsOnMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0
		h.commit("m1")   // 0.1.1
		h.commit("m2")   // 0.1.2
		h.want("0.1.2")

		h.newBranch("develop")
		h.want("0.1.2-alpha.1") // builds on the m2 boundary (0.1.2); merge-base only
		h.commit("d1")
		h.want("0.1.3-alpha.1") // patch off 0.1.2, one commit of its own

		h.newBranch("feature/x")
		h.want("0.2.0-x.0")
		h.commit("f1")
		h.want("0.2.0-x.1") // minor off the 0.1.2 main core
	})

	// A direct hotfix commit on main BEFORE a develop release merge: the hotfix
	// is a boundary (0.1.1), so develop's release merge builds on it -> 0.1.2.
	t.Run("HotfixBeforeReleaseMerge", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0
		h.commit("m1")   // hotfix on main -> 0.1.1
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		h.merge("develop") // release merge: develop tip (d1) builds on 0.1.1
		h.want("0.1.2")

		h.checkout("develop")
		h.want("0.1.2-alpha.1")
		h.commit("d2")
		h.want("0.1.3-alpha.1")
	})

	// Direct hotfix commits on main AFTER a release merge, then back-merged into
	// develop. main climbs by a patch per hotfix; the back-merge makes the latest
	// hotfix boundary (0.1.3) reachable from develop, so its section builds on it.
	t.Run("HotfixesAfterReleaseMerge", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		h.merge("develop") // release merge -> 0.1.1
		h.commit("h1")     // 0.1.2
		h.commit("h2")     // 0.1.3
		h.want("0.1.3")

		h.checkout("develop")
		h.want("0.1.1-alpha.1")
		h.backMerge()           // pulls the two hotfixes (and their boundaries) onto develop
		h.want("0.1.4-alpha.1") // builds on the 0.1.3 hotfix boundary; only the merge above it
	})

	// A semver tag placed mid-history on main is the base for commits above it;
	// each direct commit above the tag is a boundary at its patch-bumped core, so
	// develop branched from the latest main commit builds on that core (1.5.2).
	t.Run("TagMidHistoryOnMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0
		m1 := h.commit("m1")
		h.tag("1.5.0", m1) // tag on m1
		h.commit("m2")     // 1.5.1
		h.commit("m3")     // 1.5.2
		h.want("1.5.2")

		h.newBranch("develop")
		h.want("1.5.2-alpha.1") // builds on the m3 boundary (1.5.2)
		h.commit("d1")
		h.want("1.5.3-alpha.1")

		h.newBranch("feature/y")
		h.commit("f1")
		h.want("1.6.0-y.1") // minor off the 1.5.2 main core
	})

	// Multiple commits on main first, then develop is branched and advanced, then
	// more direct commits land on main and are merged into develop. develop builds
	// on the highest main core it can reach: after the merge that is m4 (0.1.4),
	// counting only its own commits and the later main commits above that boundary.
	t.Run("MainCommitsThenDevelopThenMergeMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0
		h.commit("m1")
		h.commit("m2")
		h.want("0.1.2") // two patch bumps on main

		h.newBranch("develop")
		h.commit("d1")
		h.commit("d2")
		h.want("0.1.3-alpha.2") // builds on the m2 boundary (0.1.2): d1,d2

		h.checkout("main")
		h.commit("m3")
		h.commit("m4")
		h.want("0.1.4")

		h.checkout("develop")
		h.merge("main") // pull the later main commits into develop
		// Now the highest reachable boundary is m4 (0.1.4); above it sit d1,d2 and
		// the merge commit = 3 commits, all patch -> 0.1.5-alpha.3.
		h.want("0.1.5-alpha.3")
	})

	// A tagged release, then a hotfix on main, then an untagged release merge.
	// main advances by a patch for the hotfix and again for the untagged release;
	// develop builds on the latest release core after a back-merge.
	t.Run("TaggedReleaseHotfixUntaggedRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop") // release merge
		h.tag("0.1.1", mg)       // tagged 0.1.1
		h.commit("hot1")         // hotfix -> 0.1.2 boundary, base for next release
		h.checkout("develop")
		h.commit("d2")
		h.checkout("main")
		h.merge("develop") // untagged release merge builds on the 0.1.2 hotfix
		h.want("0.1.2")

		h.checkout("develop")
		h.want("0.1.2-alpha.1")
		h.backMerge()
		h.want("0.1.3-alpha.3")
	})
}

// TestAnnotatedTags verifies that annotated tags behave identically to
// lightweight tags as release boundaries. tagCores must dereference the tag
// object to the commit it points at, so the boundary is detected on the target
// commit exactly as for a lightweight tag.
func TestAnnotatedTags(t *testing.T) {
	t.Parallel()
	// A release cut with an annotated tag is a boundary just like a lightweight
	// one: main reports the release, develop builds the next section off it.
	t.Run("ReleaseBoundary", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.annotatedTag("2.1.0", mg) // annotated, not lightweight
		h.want("2.1.0")             // on main: the release version

		h.checkout("develop")
		h.want("2.1.0-alpha.1") // no new commits since the release
		h.commit("d2")
		h.want("2.1.1-alpha.1") // next section builds on the annotated boundary
	})

	// The exact worked-example values must be reproduced when every release is
	// tagged with an annotated tag instead of a lightweight one.
	t.Run("MatchesLightweightBehavior", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")

		// Release 0.1.1 with an annotated tag.
		h.checkout("main")
		mg1 := h.merge("develop")
		h.annotatedTag("0.1.1", mg1)
		h.want("0.1.1")

		// Feature branch off develop -> minor bump built on the annotated release.
		h.checkout("develop")
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("develop")
		h.merge("feature/x")
		h.want("0.2.0-alpha.2") // minor, 2 commits since 0.1.1 boundary

		// Cut 0.2.0 with another annotated tag; main reflects it.
		h.checkout("main")
		mg2 := h.merge("develop")
		h.annotatedTag("0.2.0", mg2)
		h.want("0.2.0")
	})

	// A direct feature merge into main above an annotated release tag builds on
	// that tag and bumps minor once, exactly as with a lightweight tag.
	t.Run("DirectMergeAboveAnnotated", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.annotatedTag("1.2.0", mg) // main is now 1.2.0 via an annotated tag
		h.newBranch("feature/next")
		h.commit("f1")
		h.checkout("main")
		h.merge("feature/next")
		h.want("1.3.0") // minor bump off the annotated 1.2.0 release, once
	})
}

// TestNonSemverTagsIgnored verifies that tags whose names do not parse as semver
// are completely ignored during calculation: they never become a release
// boundary on main or develop, and they never shadow a real semver tag on the
// same commit. Only the lenient parser's rejects are exercised here — anything
// the parser accepts (including a leading "v") is intentionally still honored.
func TestNonSemverTagsIgnored(t *testing.T) {
	t.Parallel()

	// Tag names the lenient parser rejects; none of these may influence the
	// computed version.
	badTags := []string{"latest", "release-1", "foo", "1.x", "1.2.3-", "v", "nightly-2024"}

	// On main: a proper release tag sets the version; adding non-semver tags on
	// the same released commit leaves the version unchanged.
	t.Run("MainBoundaryUnaffected", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.want("2.1.0")

		for _, bad := range badTags {
			h.tag(bad, mg)
		}
		h.want("2.1.0") // still the only real tag's version
	})

	// A non-semver tag sitting on a commit with NO real semver tag must not act
	// as a boundary: the release core still falls back to the nearest real tag
	// below it (here, the 0.1.0 root default with patch bumps on main).
	t.Run("NonSemverTagIsNotABoundary", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main root -> 0.1.0
		c1 := h.commit("m1")
		h.tag("nightly-2024", c1) // non-semver: must be ignored
		h.commit("m2")
		// Two direct commits above the untagged 0.1.0 root -> 0.1.2. If the
		// non-semver tag were wrongly treated as a boundary, this would differ.
		h.want("0.1.2")
	})

	// On develop: a non-semver tag on the released tip must not be mistaken for
	// the release boundary. develop keeps building on the real semver release.
	t.Run("DevelopBoundaryUnaffected", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.tag("latest", mg) // non-semver, on the same released tip

		h.checkout("develop")
		h.want("2.1.0-alpha.1") // no new commits since the real release
		h.commit("d2")
		h.want("2.1.1-alpha.1") // next section builds on 2.1.0, not "latest"
	})

	// A non-semver tag must never shadow a real semver tag on the same commit,
	// regardless of tag creation order.
	t.Run("DoesNotShadowRealTag", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("bogus", mg) // non-semver first
		h.tag("3.4.5", mg) // real semver second
		h.tag("zzz", mg)   // and another non-semver after
		h.want("3.4.5")
	})

	// The ignored tags are traced (observable via --debug), so a dropped tag is
	// never silent.
	t.Run("IgnoredTagsAreTraced", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.tag("not-semver", mg)

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: %v", err)
		}
		if !strings.Contains(stderr, `ignoring tag "not-semver"`) {
			t.Errorf("expected trace to report the ignored tag; got:\n%s", stderr)
		}
	})

	// A pile of every kind of ignored / malformed tag (non-semver, partial,
	// leading-zero, counter-less prerelease, empty-ish) must never abort the run:
	// the app finishes successfully and reports the version from the one real
	// release tag. This is the "ignored tags never prevent calculation" guarantee.
	t.Run("PileOfBadTagsStillSucceeds", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.2.0", c1) // the one real release
		junk := []string{
			"latest", "foo", "release-99", "1.x", "1.2.3-",
			"v2", "2", "2.1", "v2.1", "1.2", // partial versions
			"01.2.3", "2020.01.15", "007.0.0", // leading zeros
			"1.2.3-rc", "9.9.9-beta", // prereleases without a counter
			"nightly", "HEAD-ish", "build_42",
		}
		for _, j := range junk {
			h.tag(j, c1)
		}
		h.commit("m2")

		out, _, err := runCaptureAll(t, h)
		if err != nil {
			t.Fatalf("run with many bad tags must succeed, got error: %v", err)
		}
		if out != "1.2.1" {
			t.Errorf("version with many bad tags = %q, want 1.2.1", out)
		}
	})
}

// TestStrictTagParsing verifies that tag parsing uses the STRICT semver parser
// after stripping a single optional leading "v". Non-strict forms — partial
// versions ("v2", "2.1") and leading-zero segments ("01.2.3", "2020.01.15") —
// are ignored, while strict releases (with or without a "v" prefix) and valid
// CalVer without leading zeros are honored.
func TestStrictTagParsing(t *testing.T) {
	t.Parallel()

	// Forms the strict parser rejects (after stripping "v") must be ignored: a
	// direct commit above the untagged 0.1.0 root reads 0.1.2 regardless.
	for _, bad := range []string{"v2", "2", "2.1", "v2.1", "01.2.3", "2020.01.15", "1.2"} {
		t.Run("Ignored/"+bad, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.commit("root") // 0.1.0 root
			c1 := h.commit("m1")
			h.tag(bad, c1) // non-strict: must be ignored
			h.commit("m2")
			h.want("0.1.2") // unaffected by the non-strict tag
		})
	}

	// A bare release with a "v" prefix is accepted (the prefix is stripped).
	t.Run("VPrefixAccepted", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("v1.9.9", c1)
		h.commit("m2")
		h.want("1.9.10") // built on 1.9.9
	})

	// A CalVer tag with no leading zeros is valid strict semver and is honored.
	t.Run("CalVerWithoutLeadingZerosAccepted", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("2020.12.15", c1)
		h.commit("m2")
		h.want("2020.12.16")
	})

	// A "v"-prefixed prerelease reference tag is accepted as a reference point.
	t.Run("VPrefixPrereleaseReference", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/x")
		h.commit("b1")
		h.tag("v4.5.6-foobar-x.3", mustHead(t, h)) // v-prefixed reference tag
		h.want("4.5.6-foobar-x.3")
	})

	// A "v"-prefixed non-strict form (partial "v2.1") used as a would-be
	// reference is also ignored.
	t.Run("VPrefixPartialIgnored", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 root
		c1 := h.commit("m1")
		h.tag("v2.1", c1) // partial after stripping "v": ignored
		h.commit("m2")
		h.want("0.1.2")
	})
}

// TestPrereleaseTagsIgnored verifies that a prerelease semver tag WITHOUT a
// trailing numeric counter (e.g. "1.2.3-rc") never establishes a release
// boundary and is otherwise ignored: a prerelease is by definition "not yet
// released", and without a counter it is not a reference point either. Build
// metadata (e.g. "1.2.3+build") is NOT a prerelease and still counts as the
// release it denotes. (Prerelease tags that DO carry a counter are reference
// tags; see TestPrereleaseReferenceTags.)
func TestPrereleaseTagsIgnored(t *testing.T) {
	t.Parallel()

	// A counter-less prerelease tag must not act as a release: two direct commits
	// above the untagged 0.1.0 root read 0.1.2, unaffected by a "5.0.0-beta" tag.
	t.Run("PrereleaseOnlyIsNotABoundary", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 root
		c1 := h.commit("m1")
		h.tag("5.0.0-beta", c1) // prerelease, no counter: must be ignored
		h.commit("m2")
		h.want("0.1.2") // NOT 5.0.1
	})

	// A counter-less prerelease tag alongside the real release on the same commit
	// must not change anything: the real release governs.
	t.Run("PrereleaseAlongsideRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.2.3-rc", c1) // prerelease, no counter: ignored
		h.tag("1.2.3", c1)    // the real release
		h.commit("m2")
		h.want("1.2.4") // built on 1.2.3, one direct commit above
	})

	// A counter-less prerelease tag on a non-main branch does not affect the
	// branch it sits on, nor develop once the branch is merged in.
	t.Run("PrereleaseOnBranchDoesNotLeak", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main") // back-merge so develop builds on 2.1.0

		// Bugfix branch with a rogue counter-less prerelease tag on its commit.
		h.newBranch("bugfix/rogue")
		h.commit("b1")
		h.tag("9.9.9-foo", mustHead(t, h)) // counter-less: ignored
		// The branch builds on the real release 2.1.0, not the rogue 9.9.9.
		h.want("2.1.1-rogue.1")

		// Merge the branch into develop: the prerelease tag still must not leak.
		h.checkout("develop")
		h.merge("bugfix/rogue")
		h.want("2.1.1-alpha.4") // still building on 2.1.0, not 9.10.0
	})

	// Build metadata is not a prerelease: "1.2.3+build" still denotes the 1.2.3
	// release and remains a valid boundary.
	t.Run("BuildMetadataStillCounts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.2.3+build.7", c1) // metadata, not prerelease: a real release
		h.commit("m2")
		h.want("1.2.4")
	})

	// The ignored counter-less prerelease tag is traced, so the drop is
	// observable.
	t.Run("PrereleaseIsTraced", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("3.0.0-alpha", c1) // no counter -> ignored
		h.commit("m2")

		_, stderr, err := runCaptureAll(t, h, "--debug")
		if err != nil {
			t.Fatalf("--debug: %v", err)
		}
		if !strings.Contains(stderr, `ignoring tag "3.0.0-alpha"`) {
			t.Errorf("expected trace to report the ignored prerelease tag; got:\n%s", stderr)
		}
	})
}

// TestPrereleaseReferenceTags verifies that a prerelease tag carrying a trailing
// numeric counter (e.g. "4.5.6-foobar-x.3") acts as a reference point rather
// than being ignored: on the tagged commit the version equals the tag verbatim,
// subsequent commits continue the counter using the tag's label, and once the
// reference core is higher than the branch's normally-computed core it takes
// over and propagates through develop (as -alpha.N) and into a main release.
func TestPrereleaseReferenceTags(t *testing.T) {
	t.Parallel()

	// On the tagged commit the version is the tag verbatim; the next commit
	// continues the counter with the tag's label.
	t.Run("VerbatimThenContinues", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/whatever")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))
		h.want("4.5.6-foobar-x.3") // exactly the tag
		h.commit("b2")
		h.want("4.5.6-foobar-x.4") // counter continues from 3, label from tag
	})

	// After the reference tag, several commits keep incrementing the counter.
	t.Run("CounterKeepsCounting", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("feature/x")
		h.commit("f1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))
		h.commit("f2")
		h.commit("f3")
		h.want("4.5.6-foobar-x.5") // 3 + two commits after the tag
	})

	// Merging the reference-tagged branch into develop yields the tag's core with
	// the alpha label and the normal develop section counter.
	t.Run("PropagatesToDevelop", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/ref")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))
		h.checkout("develop")
		h.merge("bugfix/ref")

		h.want("4.5.6-alpha.4") // tag core, alpha label, normal develop count
	})

	// Releasing that develop into main produces the tag's core as a plain
	// release.
	t.Run("PropagatesToMainRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/ref")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))
		h.checkout("develop")
		h.merge("bugfix/ref")

		h.checkout("main")
		h.merge("develop")
		h.want("4.5.6") // the reference core becomes the release
	})

	// When the branch's normally-computed core is HIGHER than the reference tag,
	// the tag is ignored entirely (core, label, and counter all come from the
	// normal computation).
	t.Run("IgnoredWhenLowerThanComputed", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("5.0.0", mg) // released 5.0.0
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/low")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h)) // lower than 5.0.x -> ignored
		// Normal computation: builds on 5.0.0, patch bump, branch label.
		h.want("5.0.1-low.1")
	})

	// Direct merge of a reference-tagged branch straight into main (no develop
	// branch, the hotfix-style flow): the reference core becomes the release.
	t.Run("DirectMergeIntoMainRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("2.1.0", mustHead(t, h))

		h.newBranch("bugfix/ref")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))
		h.want("4.5.6-foobar-x.3") // on the branch, the tag verbatim

		h.checkout("main")
		h.merge("bugfix/ref")
		h.want("4.5.6") // reference core wins over the 2.1.1 the merge would give
	})

	// Same direct-into-main flow with a feature branch. The reference tag on the
	// branch tip anchors the core to 4.5.6. The direct merge is a feature merge,
	// but it is merely the integration of the tagged branch — its automatic
	// feature-minor is exactly what the tag overrides, so it does NOT lift the
	// anchor. (Only an explicit "+semver:" on the merge message, or a signal on a
	// commit AFTER the tag, would lift it.)
	t.Run("DirectFeatureMergeIntoMainRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("2.1.0", mustHead(t, h))

		h.newBranch("feature/ref")
		h.commit("f1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))

		h.checkout("main")
		h.merge("feature/ref")
		h.want("4.5.6") // anchored; the feature merge does not lift its own tag
	})

	// Direct merge into main where the reference core is LOWER than main's
	// current release: the tag is ignored and the normal patch bump applies.
	t.Run("DirectMergeIntoMainIgnoredWhenLower", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("5.0.0", mustHead(t, h)) // main already at 5.0.0

		h.newBranch("bugfix/low")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h)) // lower -> ignored

		h.checkout("main")
		h.merge("bugfix/low")
		h.want("5.0.1") // normal patch bump, reference ignored
	})
}

// TestAnnotatedPrereleaseReferenceTag confirms an ANNOTATED prerelease reference
// tag behaves identically to a lightweight one: the annotated-tag dereference
// path (tagCores) must resolve the tag object to its target commit before
// classifying it as a reference tag. This mirrors the VerbatimThenContinues
// scenario but creates the reference tag with annotatedTag instead of tag.
func TestAnnotatedPrereleaseReferenceTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("2.1.0", mg)
	h.checkout("develop")
	h.merge("main")

	h.newBranch("bugfix/whatever")
	h.commit("b1")
	h.annotatedTag("4.5.6-foobar-x.3", mustHead(t, h)) // annotated, not lightweight
	h.want("4.5.6-foobar-x.3")                         // exactly the tag
	h.commit("b2")
	h.want("4.5.6-foobar-x.4") // counter continues from 3, label from tag
}

// TestReferenceTagDownwardAnchor verifies that a prerelease reference tag can
// pull a computed core DOWN (revert an automatic bump), not just raise it, as
// long as the anchored core stays at or above the release boundary the section
// builds on. Commits after the tag only advance the counter unless they carry an
// explicit "+semver:" directive (or are a feature merge), which lifts the anchor.
func TestReferenceTagDownwardAnchor(t *testing.T) {
	t.Parallel()

	// The headline scenario: last main release 1.2.2, a feature merged into
	// develop would bump minor to 1.3.0, but a reference tag 1.2.3-alpha.N on
	// that merge reverts it to a patch-range 1.2.3. New develop commits keep the
	// 1.2.3 core and only advance the counter; releasing to main yields 1.2.3.
	t.Run("RevertFeatureMinorToPatchOnDevelop", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("1.2.2", mg) // main released at 1.2.2
		h.checkout("develop")
		h.merge("main")

		h.newBranch("feature/big")
		h.commit("f1")
		h.checkout("develop")
		fm := h.merge("feature/big") // would bump minor -> 1.3.0
		h.want("1.3.0-alpha.4")

		// Pin it back down to the patch range with a reference tag on the merge.
		// On develop the label is always "alpha" and the counter is the develop
		// section commit count (4 here), NOT the tag's own counter (.2) — only
		// the tag's CORE is used, matching PropagatesToDevelop's convention.
		h.tag("1.2.3-alpha.2", fm)
		h.want("1.2.3-alpha.4") // reverted core 1.2.3; develop section count = 4

		// New develop commits keep the 1.2.3 core, only the counter advances.
		h.commit("d2")
		h.want("1.2.3-alpha.5")

		// Releasing that develop into main yields exactly 1.2.3.
		h.checkout("main")
		h.merge("develop")
		h.want("1.2.3")
	})

	// A "+semver: major" on a develop commit AFTER the reference tag lifts the
	// anchor above the tag's core; a plain commit would not.
	t.Run("MarkerAfterTagRaisesAnchor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("1.2.2", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("feature/big")
		h.commit("f1")
		h.checkout("develop")
		fm := h.merge("feature/big")
		h.tag("1.2.3-alpha.2", fm) // anchor to 1.2.3
		h.want("1.2.3-alpha.4")

		h.commit("plain") // plain commit: counter only
		h.want("1.2.3-alpha.5")
		h.commit("break +semver: major") // explicit marker after tag: lifts anchor
		h.want("2.0.0-alpha.6")
	})

	// An independent feature merge that landed on develop's mainline PARALLEL to
	// the tagged branch (neither is an ancestor of the other) is not "after the
	// tag" and must not lift the anchor. Here feature/foo is tagged 1.2.3-foo.5
	// on a side branch while feature/par is merged into develop in parallel; when
	// feature/foo is merged the version stays 1.2.3, not 1.3.0.
	t.Run("ParallelFeatureMergeDoesNotLiftAnchor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("0.56.0", mg)
		h.checkout("develop")
		h.merge("main")

		// feature/foo forks and is tagged, with a plain commit after the tag.
		h.newBranch("feature/foo")
		h.commit("ff1")
		h.tag("1.2.3-foo.5", mustHead(t, h))
		h.commit("ff2")

		// Meanwhile an independent feature branch is merged into develop.
		h.checkout("develop")
		h.newBranch("feature/par")
		h.commit("p1")
		h.checkout("develop")
		h.merge("feature/par")

		// Merging the tagged branch keeps 1.2.3: the parallel feature merge does
		// not lift it.
		h.merge("feature/foo")
		h.want("1.2.3-alpha.7")

		// But a feature merge that lands AFTER the anchor (a descendant of the tag
		// merge) DOES lift it to a minor: 1.3.0.
		h.newBranch("feature/next")
		h.commit("n1")
		h.checkout("develop")
		h.merge("feature/next")
		h.want("1.3.0-alpha.9")
	})

	// When the anchor stays at 1.2.3 (a parallel feature merge did not lift it),
	// releasing develop into main publishes exactly 1.2.3 — the release inherits
	// the un-lifted anchored core.
	t.Run("UnliftedAnchorReleasesAsPatch", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("0.56.0", mg)
		h.checkout("develop")
		h.merge("main")

		// feature/foo forks and is tagged, with a plain commit after the tag.
		h.newBranch("feature/foo")
		h.commit("ff1")
		h.tag("1.2.3-foo.5", mustHead(t, h))
		h.commit("ff2")

		// An independent feature branch is merged into develop in parallel.
		h.checkout("develop")
		h.newBranch("feature/par")
		h.commit("p1")
		h.checkout("develop")
		h.merge("feature/par")

		// Merging the tagged branch keeps 1.2.3 (parallel merge does not lift).
		h.merge("feature/foo")
		h.want("1.2.3-alpha.7")

		// Releasing develop into main publishes the un-lifted anchor: 1.2.3.
		h.checkout("main")
		h.merge("develop")
		h.want("1.2.3")
	})

	// A feature merge after the tag lifts the anchor to minor regardless of when
	// the independent branch was CREATED (before or after the tagged branch) —
	// what matters is that it is merged AFTER the anchor (as its descendant), not
	// its fork point. Here the independent branch is created BEFORE the tagged
	// branch but merged after it.
	t.Run("IndependentFeatureCreatedBeforeMergedAfterLifts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("0.56.0", mg)
		h.checkout("develop")
		h.merge("main")

		// Independent feature branch created FIRST, left unmerged for now.
		h.newBranch("feature/par")
		h.commit("p1")

		// feature/foo created and tagged, then merged to establish the anchor.
		h.checkout("develop")
		h.newBranch("feature/foo")
		h.commit("ff1")
		h.tag("1.2.3-foo.5", mustHead(t, h))
		h.commit("ff2")
		h.checkout("develop")
		h.merge("feature/foo")
		h.want("1.2.3-alpha.5") // anchor established

		// Now merge the older independent branch: it lands after the anchor and
		// lifts to minor.
		h.merge("feature/par")
		h.want("1.3.0-alpha.7")
	})

	// The same, but the independent branch is created AFTER the tagged branch was
	// merged. It still lifts to minor.
	t.Run("IndependentFeatureCreatedAfterMergedAfterLifts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("0.56.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("feature/foo")
		h.commit("ff1")
		h.tag("1.2.3-foo.5", mustHead(t, h))
		h.commit("ff2")
		h.checkout("develop")
		h.merge("feature/foo")
		h.want("1.2.3-alpha.5") // anchor established

		// Independent feature branch created AFTER feature/foo was merged.
		h.newBranch("feature/par")
		h.commit("p1")
		h.checkout("develop")
		h.merge("feature/par")
		h.want("1.3.0-alpha.7")
	})

	// Multiple raise signals after the tag still lift the anchor only ONCE: the
	// strongest single bump applies, extra signals of the same or lower strength
	// only advance the counter. Here two feature merges and a "+semver: minor"
	// after a 1.2.3 anchor together yield a single minor step to 1.3.0, not more.
	t.Run("MultipleRaisesAfterTagLiftOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("1.2.2", mg)
		h.checkout("develop")
		h.merge("main")

		// Anchor develop to 1.2.3 on a plain develop commit.
		dc := h.commit("d2")
		h.tag("1.2.3-alpha.2", dc)
		h.want("1.2.3-alpha.3") // reverted to the 1.2.3 anchor

		// First feature merge after the tag lifts the anchor to minor.
		h.newBranch("feature/one")
		h.commit("f1")
		h.checkout("develop")
		h.merge("feature/one")
		h.want("1.3.0-alpha.5") // single minor step

		// A second feature merge after the tag must NOT bump again: still minor,
		// only the counter grows.
		h.newBranch("feature/two")
		h.commit("f2")
		h.checkout("develop")
		h.merge("feature/two")
		h.want("1.3.0-alpha.7") // still 1.3.0, counter only

		// An explicit "+semver: minor" after the tag is the same strength as the
		// feature merges: still a single minor step, counter only.
		h.commit("more work +semver: minor")
		h.want("1.3.0-alpha.8") // still 1.3.0
	})

	// A stronger signal after the tag wins over weaker ones, but still lifts the
	// anchor exactly once: a "+semver: major" among several minor signals yields
	// a single major step, not major-then-minor.
	t.Run("StrongestRaiseAfterTagWinsOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("1.2.2", mg)
		h.checkout("develop")
		h.merge("main")

		dc := h.commit("d2")
		h.tag("1.2.3-alpha.2", dc) // anchor to 1.2.3
		h.want("1.2.3-alpha.3")

		// A feature merge (minor) and a "+semver: major" both after the tag:
		// the major dominates and applies once -> 2.0.0, not 2.1.0.
		h.newBranch("feature/one")
		h.commit("f1")
		h.checkout("develop")
		h.merge("feature/one")
		h.want("1.3.0-alpha.5") // minor so far

		h.commit("breaking +semver: major")
		h.want("2.0.0-alpha.6") // single major step, minor absorbed

		h.commit("another break +semver: major")
		h.want("2.0.0-alpha.7") // still 2.0.0, counter only
	})

	// Rule 2 for non-main/non-develop branches: the downward anchor is bounded by
	// the boundary the branch builds on. Here main already released 2.0.0, so a
	// 1.2.3 reference tag on a bugfix branch is BELOW the boundary and is ignored;
	// the branch (and any later merge) must not produce an incorrect low bump.
	t.Run("BlockedByHigherBoundaryOnBranch", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.0.0", mg) // main already at 2.0.0
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/low")
		h.commit("b1")
		h.tag("1.2.3-foobar.2", mustHead(t, h)) // below 2.0.0 boundary -> ignored
		// Branch builds on 2.0.0, patch bump, branch label.
		h.want("2.0.1-low.1")

		// Merging into develop must also not drop below the boundary.
		h.checkout("develop")
		h.merge("bugfix/low")
		h.want("2.0.1-alpha.4")
	})

	// The anchor works on a non-develop branch when it is at or above the
	// boundary: a bugfix branch off a 1.2.2 main, tagged 1.2.3-foobar.N, versions
	// as 1.2.3 verbatim (tag label + counter), reverting the branch's own bump.
	t.Run("AnchorOnBranchAboveBoundary", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h)) // main at 1.2.2 (boundary)

		h.newBranch("feature/x")
		h.commit("f1") // feature branch would bump minor -> 1.3.0
		h.tag("1.2.3-foobar.2", mustHead(t, h))
		h.want("1.2.3-foobar.2") // anchored to 1.2.3, reverting the minor bump

		h.commit("f2") // plain commit: counter only
		h.want("1.2.3-foobar.3")
	})

	// On a feature branch reverted to a patch anchor, merging ANOTHER feature
	// branch into it lifts the anchor back to a minor bump: the incoming feature
	// merge carries the weight of "+semver: minor" and sits above the tag.
	t.Run("FeatureMergeIntoBranchLiftsAnchor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h)) // main at 1.2.2 (boundary)

		// A sibling feature branch to merge in later.
		h.newBranch("feature/b")
		h.commit("b1")

		// The working feature branch: reverted from minor to patch by a tag.
		h.checkout("main")
		h.newBranch("feature/a")
		h.commit("a1")
		h.tag("1.2.3-mywork.2", mustHead(t, h))
		h.want("1.2.3-mywork.2") // anchored to 1.2.3

		h.commit("a2") // plain commit: counter only, still patch range
		h.want("1.2.3-mywork.3")

		// Merge the other feature branch in: lifts the anchor back to minor.
		h.merge("feature/b")
		h.want("1.3.0-mywork.5") // feature merge above the tag -> minor
	})

	// On the branch path too, multiple raise signals after the tag lift the
	// anchor only ONCE. Two feature merges after a 1.2.3 anchor on a branch yield
	// a single minor step to 1.3.0; the second only advances the counter (which
	// continues from the tag's counter plus commits after it).
	t.Run("MultipleRaisesAfterTagLiftOnceOnBranch", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h)) // main at 1.2.2 (boundary)

		// Two sibling feature branches to merge in later.
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("main")
		h.newBranch("feature/c")
		h.commit("c1")

		// The working feature branch, reverted to a 1.2.3 anchor.
		h.checkout("main")
		h.newBranch("feature/a")
		h.commit("a1")
		h.tag("1.2.3-mywork.2", mustHead(t, h))
		h.want("1.2.3-mywork.2") // anchored to 1.2.3

		// First feature merge lifts to a single minor step.
		h.merge("feature/b")
		h.want("1.3.0-mywork.4") // tag counter 2 + (merge + b1) = 4

		// Second feature merge must NOT bump again: still minor, counter grows.
		h.merge("feature/c")
		h.want("1.3.0-mywork.6") // still 1.3.0; tag counter 2 + 4 after = 6
	})

	// A plain (non-feature) merge into the reverted feature branch does NOT lift
	// the anchor: it behaves like an ordinary commit, advancing the counter only.
	t.Run("PlainMergeIntoBranchKeepsAnchor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))

		h.newBranch("bugfix/b")
		h.commit("b1")

		h.checkout("main")
		h.newBranch("feature/a")
		h.commit("a1")
		h.tag("1.2.3-mywork.2", mustHead(t, h))
		h.want("1.2.3-mywork.2")

		h.merge("bugfix/b")      // non-feature merge: no explicit signal, counter only
		h.want("1.2.3-mywork.4") // core stays 1.2.3
	})

	// Direct feature merge into main: a reference tag on the branch TIP anchors
	// the core to 1.2.3. The direct merge is a feature merge, but it is only the
	// integration of the tagged branch, so its automatic feature-minor does NOT
	// lift the anchor — the tag reverts the branch's own bump.
	t.Run("FeatureMergeOfTaggedTipDoesNotLift", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))

		h.newBranch("feature/ref")
		h.commit("f1")
		h.tag("1.2.3-foobar.2", mustHead(t, h)) // on the branch tip
		h.checkout("main")
		h.merge("feature/ref")
		h.want("1.2.3") // anchored; the feature merge does not lift its own tag
	})

	// But a signal on a commit AFTER the tag inside the merged branch DOES lift
	// the anchor: here a feature branch is merged into the ref-tagged branch above
	// the tag, then the whole thing is merged into main.
	t.Run("SignalAfterTagInMergedBranchLifts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))

		// A feature branch to merge into the ref-tagged branch later.
		h.newBranch("feature/inner")
		h.commit("i1")

		h.checkout("main")
		h.newBranch("bugfix/ref")
		h.commit("b1")
		h.tag("1.2.3-foobar.2", mustHead(t, h)) // anchor on the branch
		h.merge("feature/inner")                // feature merge AFTER the tag lifts it
		h.want("1.3.0-foobar.4")

		h.checkout("main")
		h.merge("bugfix/ref")
		h.want("1.3.0") // in-branch feature merge after the tag lifted the anchor
	})

	// To also revert when the merge commit itself should carry the release core,
	// the reference tag can sit on the MERGE COMMIT: nothing is above it, so its
	// core stands as the release.
	t.Run("RevertFeatureMergeByTaggingMergeCommit", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))

		h.newBranch("feature/ref")
		h.commit("f1")
		h.checkout("main")
		mc := h.merge("feature/ref") // feature merge would bump minor -> 1.3.0
		h.tag("1.2.3-foobar.2", mc)  // tag ON the merge commit reverts it
		h.want("1.2.3")
	})
}

// TestReferenceTagPatchPropagation verifies that a feature branch lowered to a
// patch by a reference tag (with nothing restoring it) carries that patch
// verdict through a merge into develop and on to a release, exactly as if a
// bugfix branch had been merged — unless a "+semver:" directive on a merge
// commit overrides it. It also verifies the boundaries of that propagation: a
// feature branch that RECEIVES such a tagged branch keeps its own minor, and a
// fresh feature branch cut from a now-patched develop has its own minor.
func TestReferenceTagPatchPropagation(t *testing.T) {
	t.Parallel()

	// setup builds main=1.2.2 with develop synced to it, returning the harness on
	// develop.
	setup := func(t *testing.T) *harness {
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("1.2.2", mg)
		h.checkout("develop")
		h.merge("main")
		return h
	}

	// A feature branch capped to patch, merged plainly into develop, keeps the
	// patch: develop and the subsequent release are 1.2.3, not 1.3.0.
	t.Run("PatchFeatureIntoDevelopStaysPatch", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.tag("1.2.3-x.2", mustHead(t, h))
		h.checkout("develop")
		h.merge("feature/x")    // plain merge, no +semver
		h.want("1.2.3-alpha.4") // patch inherited from the capped feature branch

		h.checkout("main")
		h.merge("develop")
		h.want("1.2.3") // patch release
	})

	// A reference tag that sits on a feature branch and is NOT its tip (there are
	// plain commits after it on the same branch) is the branch's own final
	// version. Merging that branch into develop must keep the anchored core: the
	// feature merge's implicit minor is the same work the tag already reflects, so
	// it must not re-lift 1.2.3 -> 1.3.0. The release then also stays 1.2.3.
	t.Run("RefTagOnFeatureBranchHeldThroughMerge", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/foo")
		h.commit("f1")
		h.tag("1.2.3-foo.5", mustHead(t, h))
		h.commit("f2") // plain commit after the tag: counter only
		h.want("1.2.3-foo.6")
		h.checkout("develop")
		h.merge("feature/foo")  // plain feature merge integrating the tagged branch
		h.want("1.2.3-alpha.5") // stays 1.2.3, NOT 1.3.0
		h.checkout("main")
		h.merge("develop")
		h.want("1.2.3")
	})

	// The same topology, but the integrating merge carries an explicit
	// "+semver: minor": that deliberate override still lifts the anchor to 1.3.0,
	// unlike the implicit feature-merge minor which is suppressed.
	t.Run("ExplicitSemverOnIntegratingMergeStillLifts", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/foo")
		h.commit("f1")
		h.tag("1.2.3-foo.5", mustHead(t, h))
		h.commit("f2")
		h.checkout("develop")
		h.mergeMsg("feature/foo", "Merge branch 'feature/foo'\n\n+semver: minor")
		h.want("1.3.0-alpha.5") // explicit directive overrides the held anchor
	})

	// A "+semver: minor" on the develop merge commit overrides the inherited
	// patch and restores the minor.
	t.Run("SemverOnDevelopMergeRestoresMinor", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.tag("1.2.3-x.2", mustHead(t, h))
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: minor")
		h.want("1.3.0-alpha.4") // +semver on the merge restores minor

		h.checkout("main")
		h.merge("develop")
		h.want("1.3.0")
	})

	// A feature branch that RECEIVES a patch-capped feature branch keeps its OWN
	// minor: the merged-in tag does not cap the receiver.
	t.Run("PatchFeatureIntoFeatureKeepsMinor", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/a")
		h.commit("a1")
		h.tag("1.2.3-a.2", mustHead(t, h)) // feature/a capped to patch
		h.checkout("develop")
		h.newBranch("feature/b")
		h.commit("b1")
		h.merge("feature/a") // merge the capped branch into feature/b
		h.want("1.3.0-b.3")  // feature/b keeps its own minor

		h.checkout("develop")
		h.merge("feature/b")
		h.want("1.3.0-alpha.6") // minor propagates to develop
	})

	// A fresh feature branch cut from a develop that was patched by a capped
	// feature merge has its own minor.
	t.Run("NewFeatureFromPatchedDevelopHasMinor", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.tag("1.2.3-x.2", mustHead(t, h))
		h.checkout("develop")
		h.merge("feature/x")
		h.want("1.2.3-alpha.4") // develop patched

		h.newBranch("feature/y")
		h.commit("y1")
		h.want("1.3.0-y.1") // fresh feature branch: own minor

		h.checkout("develop")
		h.merge("feature/y")
		h.want("1.3.0-alpha.6") // minor from the new feature branch
	})

	// A patch-capped feature branch merged directly into main (no develop) is a
	// patch release.
	t.Run("PatchFeatureDirectToMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))

		h.newBranch("feature/x")
		h.commit("f1")
		h.tag("1.2.3-x.2", mustHead(t, h))
		h.checkout("main")
		h.merge("feature/x")
		h.want("1.2.3") // patch release, feature minor reverted by the tag
	})
}

// TestConflictingTags verifies the rules for commits carrying tags that resolve
// to more than one version:
//  1. A conflict only errors when the ambiguous commit is actually relevant to
//     the computed version; a conflict on an older commit that a later clean tag
//     supersedes is ignored.
//  2. When relevant, a conflict errors regardless of whether the tags are
//     releases or prereleases, and regardless of the branch.
//  3. Two tags resolving to the SAME version (e.g. "1.2.3" and "v1.2.3", or a
//     release and its build-metadata variant) are not a conflict.
func TestConflictingTags(t *testing.T) {
	t.Parallel()

	// (1) A conflict below a later clean release tag is irrelevant and ignored.
	t.Run("IrrelevantEarlierConflictIgnored", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.0.0", c1) // conflicting pair on m1...
		h.tag("4.0.0", c1) // ...but m1 is superseded below the latest tag
		c2 := h.commit("m2")
		h.tag("5.0.0", c2) // latest clean release governs
		h.commit("m3")
		h.want("5.0.1") // built on 5.0.0; the m1 conflict never consulted
	})

	// (2a) Two different RELEASE tags on the relevant commit error.
	t.Run("ReleaseConflictErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.0.0", c1)
		h.tag("4.0.0", c1)
		_, _, err := runCaptureAll(t, h)
		if err == nil || !strings.Contains(err.Error(), "conflicting version tags") {
			t.Fatalf("expected conflicting version tags error, got: %v", err)
		}
	})

	// (2b) A release tag vs a prerelease reference tag (different versions) on
	// the relevant commit errors.
	t.Run("ReleaseVsPrereleaseConflictErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.0.0", c1)
		h.tag("4.5.6-foo.2", c1)
		_, _, err := runCaptureAll(t, h)
		if err == nil || !strings.Contains(err.Error(), "conflicting version tags") {
			t.Fatalf("expected conflicting version tags error, got: %v", err)
		}
	})

	// (2c) A conflict on a non-main branch's own HEAD commit errors.
	t.Run("ConflictOnBranchErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/x")
		h.commit("b1")
		head := mustHead(t, h)
		h.tag("4.5.6-foo.2", head)
		h.tag("7.8.9-bar.3", head)
		_, _, err := runCaptureAll(t, h)
		if err == nil || !strings.Contains(err.Error(), "conflicting version tags") {
			t.Fatalf("expected conflicting version tags error, got: %v", err)
		}
	})

	// (2d) A conflict between two release tags on develop's released tip errors.
	t.Run("ConflictOnDevelopErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.tag("9.9.9", mg) // conflicting release on the released tip
		h.checkout("develop")

		_, _, err := runCaptureAll(t, h)
		if err == nil || !strings.Contains(err.Error(), "conflicting version tags") {
			t.Fatalf("expected conflicting version tags error on develop, got: %v", err)
		}
	})

	// (3a) "1.2.3" and "v1.2.3" resolve to the same version: no conflict.
	t.Run("VPrefixDuplicateNotAConflict", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.2.3", c1)
		h.tag("v1.2.3", c1)
		h.commit("m2")
		h.want("1.2.4")
	})

	// (3b) A release and its build-metadata variant are the same release.
	t.Run("BuildMetadataDuplicateNotAConflict", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		c1 := h.commit("m1")
		h.tag("1.2.3", c1)
		h.tag("1.2.3+build.9", c1)
		h.commit("m2")
		h.want("1.2.4")
	})

	// (3c) A prerelease reference tag and its "v"-prefixed duplicate are the same
	// reference: no conflict.
	t.Run("VPrefixPrereleaseDuplicateNotAConflict", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		h.newBranch("bugfix/x")
		h.commit("b1")
		head := mustHead(t, h)
		h.tag("4.5.6-foo.2", head)
		h.tag("v4.5.6-foo.2", head)
		h.want("4.5.6-foo.2")
	})
}

// TestDirectMergeIntoMain verifies how a merge of a non-develop branch directly
// into main advances the release core: a feature branch bumps minor, any other
// branch bumps patch, each exactly once — unless a commit brought in by the
// merge requests a stronger bump.
func TestDirectMergeIntoMain(t *testing.T) {
	t.Parallel()
	// A feature branch merged straight into main bumps minor, once.
	t.Run("FeatureMinorOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.checkout("main")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.commit("f2")
		h.commit("f3")
		h.want("0.2.0-cool-abc.3")
		h.checkout("main")
		h.merge("feature/cool-abc") // "Merge branch 'feature/cool-abc'"
		h.want("0.2.0")             // minor bumped ONCE (not once per feature commit)
		h.checkout("develop")
		h.want("0.1.0-alpha.0")
		h.backMerge()
		h.want("0.3.0-alpha.2")
		h.commit("d1")
		h.want("0.3.0-alpha.3")
	})

	// The "feat/" shorthand is treated exactly like "feature/": the branch takes
	// a minor increment, and merging it into main bumps minor once.
	t.Run("FeatShorthandMinorOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.checkout("main")
		h.newBranch("feat/cool-abc")
		h.commit("f1")
		h.commit("f2")
		h.commit("f3")
		h.want("0.2.0-cool-abc.3") // minor increment from the feat/ prefix
		h.checkout("main")
		h.merge("feat/cool-abc") // "Merge branch 'feat/cool-abc'"
		h.want("0.2.0")          // minor bumped ONCE via the feat/ merge
		h.checkout("develop")
		h.want("0.1.0-alpha.0")
		h.backMerge()
		h.want("0.3.0-alpha.2")
		h.commit("d1")
		h.want("0.3.0-alpha.3")
	})

	t.Run("FeatureMinorOnceWithTag", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		mg := h.commit("root") // main = 0.1.0
		h.tag("0.1.0", mg)
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.checkout("main")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.commit("f2")
		h.commit("f3")
		h.want("0.2.0-cool-abc.3")
		h.checkout("main")
		mg = h.merge("feature/cool-abc") // "Merge branch 'feature/cool-abc'"
		h.want("0.2.0")                  // minor bumped ONCE (not once per feature commit)
		h.tag("0.2.0", mg)
		h.checkout("develop")
		h.want("0.1.0-alpha.0")
		h.backMerge()
		h.want("0.3.0-alpha.2")
		h.commit("d1")
		h.want("0.3.0-alpha.3")
	})

	// A non-feature branch merged straight into main bumps patch, once.
	t.Run("BugfixPatchOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.checkout("main")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.commit("b2")
		h.want("0.1.1-ABC-9.2")
		h.checkout("main")
		h.merge("bugfix/ABC-9")
		h.want("0.1.1") // patch bumped ONCE
		h.checkout("develop")
		h.want("0.1.0-alpha.0")
		h.backMerge()
		h.want("0.1.2-alpha.2")
		h.commit("d1")
		h.want("0.1.2-alpha.3")
	})

	t.Run("BugfixPatchOnceWithTag", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		mg := h.commit("root") // main = 0.1.0
		h.tag("0.1.0", mg)
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.checkout("main")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.commit("b2")
		h.want("0.1.1-ABC-9.2")
		h.checkout("main")
		h.merge("bugfix/ABC-9")
		h.want("0.1.1") // patch bumped ONCE
		h.checkout("develop")
		h.want("0.1.0-alpha.0")
		h.backMerge()
		h.want("0.1.2-alpha.2")
		h.commit("d1")
		h.want("0.1.2-alpha.3")
	})

	t.Run("BugfixPatchOnceWithTagAndExtraDevelopCommit", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		mg := h.commit("root") // main = 0.1.0
		h.tag("0.1.0", mg)
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.commit("d1")
		h.want("0.1.1-alpha.1")
		h.checkout("main")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.commit("b2")
		h.checkout("main")
		h.merge("bugfix/ABC-9")
		h.want("0.1.1") // patch bumped ONCE
		h.checkout("develop")
		h.want("0.1.1-alpha.1")
		h.backMerge()
		h.want("0.1.2-alpha.3")
		h.commit("d1")
		h.want("0.1.2-alpha.4")
	})

	// A "+semver: major" on a commit in a feature branch overrides the minor
	// floor: the whole merge still advances the core exactly once, to major.
	t.Run("FeatureWithMajorCommit", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("feature/big")
		h.commit("f1")
		h.commit("breaking change +semver: major")
		h.checkout("main")
		h.merge("feature/big")
		h.want("1.0.0") // major from the merged commit, applied once
	})

	// A "+semver: minor" on a commit in a non-feature branch overrides the patch
	// floor, once.
	t.Run("BugfixWithMinorCommit", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("hotfix/urgent")
		h.commit("h1")
		h.commit("adds a feature really +semver: minor")
		h.checkout("main")
		h.merge("hotfix/urgent")
		h.want("0.2.0") // minor from the merged commit, applied once
	})

	// A "+semver: major" in the MERGE COMMIT'S OWN message (not on a commit
	// inside the merged branch) raises a feature merge's minor floor to major.
	t.Run("FeatureMergeMessageMajor1", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("feature/big")
		h.commit("f1")
		h.commit("f2")
		h.checkout("main")
		h.mergeMsg("feature/big", "Merge branch 'feature/big'\n\n+semver: major")
		h.want("1.0.0") // major from the merge commit message, applied once
	})

	// A "+semver: minor" in the merge commit's own message raises a non-feature
	// (patch-floor) merge to minor.
	t.Run("BugfixMergeMessageMinor1", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("hotfix/urgent")
		h.commit("h1")
		h.checkout("main")
		h.mergeMsg("hotfix/urgent", "Merge branch 'hotfix/urgent'\n\n+semver: minor")
		h.want("0.2.0") // minor from the merge commit message, applied once
	})

	// Without merge commit part in the message
	t.Run("FeatureMergeMessageMajor2", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("feature/big")
		h.commit("f1")
		h.commit("f2")
		h.checkout("main")
		h.mergeMsg("feature/big", "+semver: major")
		h.want("1.0.0") // major from the merge commit message, applied once
	})

	// Without merge commit part in the message
	t.Run("BugfixMergeMessageMinor2", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("hotfix/urgent")
		h.commit("h1")
		h.checkout("main")
		h.mergeMsg("hotfix/urgent", "+semver: minor")
		h.want("0.2.0") // minor from the merge commit message, applied once
	})

	// A "+semver: minor" in the merge message does not lower a feature merge's
	// minor floor, nor does it stack a second increment: the result is minor.
	t.Run("FeatureMergeMessageMinorNoStack", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("main")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: minor")
		h.want("0.2.0") // minor floor and minor marker agree: bumped once
	})

	// A "+semver: major" in the merge message is exact and overrides a weaker
	// "+semver: minor" on a commit inside the merged branch.
	t.Run("MergeMessageBeatsInnerCommit", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("hotfix/urgent")
		h.commit("h1 +semver: minor")
		h.checkout("main")
		h.mergeMsg("hotfix/urgent", "Merge branch 'hotfix/urgent'\n\n+semver: major")
		h.want("1.0.0") // major from the merge message dominates the inner minor
	})

	// A direct merge into main above an existing release tag builds on that tag
	// and still advances only once.
	t.Run("FeatureAfterRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.release("1.2.0") // main is now 1.2.0
		h.checkout("main")
		h.newBranch("feature/next")
		h.commit("f1")
		h.checkout("main")
		h.merge("feature/next")
		h.want("1.3.0") // minor bump off the 1.2.0 release, once
	})

	t.Run("BugfixPatchAndSeparateDevelopFeatureBump", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		mg := h.commit("root") // main = 0.1.0
		h.tag("0.1.0", mg)
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.newBranch("feature/next")
		h.commit("f1")
		h.checkout("develop")
		h.merge("feature/next")
		h.want("0.2.0-alpha.2")
		h.checkout("main")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.checkout("main")
		mg = h.merge("bugfix/ABC-9")
		h.want("0.1.1")
		h.tag("0.1.1", mg)
		h.want("0.1.1")
		h.checkout("develop")
		h.want("0.2.0-alpha.2")
		h.backMerge()
		h.want("0.2.0-alpha.4")
		h.commit("d1")
		h.want("0.2.0-alpha.5")
	})

	t.Run("BugfixPatchAndSeparateDevelopFeatureBumpWithTag", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.want("0.1.0-alpha.0")
		h.newBranch("feature/next")
		h.commit("f1")
		h.checkout("develop")
		h.merge("feature/next")
		h.want("0.2.0-alpha.2")
		h.checkout("main")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.checkout("main")
		h.merge("bugfix/ABC-9")
		h.want("0.1.1")
		h.checkout("develop")
		h.want("0.2.0-alpha.2")
		h.backMerge()
		h.want("0.2.0-alpha.4")
		h.commit("d1")
		h.want("0.2.0-alpha.5")
	})
}

func TestMainSemverOverrides(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.want("0.1.0")
	h.commit("minor please +semver: minor")
	h.want("0.2.0")
	h.commit("major please +semver: major")
	h.want("1.0.0")
	h.commit("plain")
	h.want("1.0.1")
}

// TestTagOverridesCalculatedVersion verifies that a semver tag on main is the
// source of truth for the release core: when the tagged value disagrees with
// what genver would otherwise calculate (whether the tag is higher OR lower than
// the calculated version), main reports the tag, and every derived calculation
// on develop and on other branches builds on the tagged core rather than on the
// calculated one.
func TestTagOverridesCalculatedVersion(t *testing.T) {
	t.Parallel()
	// A tag HIGHER than the calculated value wins on main, and develop/feature
	// derivations build on it.
	t.Run("HigherTagOnMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		root := h.commit("root")
		h.want("0.1.0") // calculated: root is the 0.1.0 boundary
		h.tag("3.4.5", root)
		h.want("3.4.5") // tag overrides the calculated 0.1.0

		// develop off the tagged commit builds on the tag core, not 0.1.0.
		h.newBranch("develop")
		h.want("3.4.5-alpha.0")
		h.commit("d1")
		h.want("3.4.6-alpha.1") // patch bump off the tag, not off 0.1.0

		// A feature branch derives its minor bump from the tag core.
		h.newBranch("feature/x")
		h.commit("f1")
		h.want("3.5.0-x.1") // 3.4.5 + minor, not 0.2.0
	})

	// A tag LOWER than the calculated value still wins: the +semver-driven
	// calculation (which would reach 2.0.0) is overridden by the 0.3.0 tag, and
	// develop, feature, and bugfix derivations all build on the lower 0.3.x core
	// rather than the untagged 2.0.x.
	t.Run("LowerTagOnMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0
		h.commit("breaking +semver: major")
		h.commit("breaking again +semver: major")
		h.want("2.0.0") // calculated: 0.1.0 -> 1.0.0 -> 2.0.0
		c2, _ := h.g.headCommit()
		h.tag("0.3.0", c2.Hash)
		h.want("0.3.0") // tag overrides the calculated 2.0.0 downward

		// A subsequent commit on main continues from the tag, not from 2.0.0.
		h.commit("plain")
		h.want("0.3.1")

		// develop off the tagged commit builds on 0.3.0, and its own commits keep
		// advancing from the LOWER core, never resurfacing the untagged 2.0.x.
		h.checkout("main")
		h.newBranch("develop")
		h.want("0.3.1-alpha.1") // one commit ("plain") above the 0.3.0 boundary
		h.commit("d1")
		h.want("0.3.2-alpha.1") // patch off 0.3.1, not off 2.0.0
		h.commit("d2")
		h.want("0.3.2-alpha.2")

		// A feature branch derives its minor from the lower tag core: 0.4.0, NOT
		// 2.1.0 (the untagged core) and NOT 0.2.0 (root). This is where ignoring
		// the boundary would diverge most visibly.
		h.newBranch("feature/x")
		h.commit("f1")
		h.want("0.4.0-x.1")

		// A bugfix branch derives a patch off the same lower core.
		h.checkout("develop")
		h.newBranch("bugfix/y")
		h.commit("b1")
		h.want("0.3.2-y.1")
	})

	// A tag on a release merge commit overrides the calculated release core, and
	// develop plus a feature branch off it build on the tagged core.
	t.Run("TagOnReleaseMergeDerives", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.want("0.1.1") // calculated release core
		h.tag("9.9.9", mg)
		h.want("9.9.9") // tag overrides the calculated 0.1.1

		// develop keeps the tagged core (no new commits since the release).
		h.checkout("develop")
		h.want("9.9.9-alpha.1")

		// A feature branch off develop derives its minor bump from 9.9.9.
		h.newBranch("feature/z")
		h.commit("f1")
		h.want("9.10.0-z.1") // 9.9.9 + minor, not 0.2.0

		// Back-merging main into develop still builds on the tagged 9.9.9 core.
		h.checkout("develop")
		h.backMerge()
		h.want("9.9.10-alpha.2")
	})
}

// TestFeatureMajorOverride verifies a "+semver: major" inside a feature merge
// wins over the feature branch's default minor bump.
func TestDevelopMajorOnDirectCommit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("breaking +semver: major")
	h.want("1.0.0-alpha.1")
	h.checkout("main")
	h.merge("develop")
	h.want("1.0.0")
}

// TestFeatureMergeMessageFormats verifies that a feature-branch merge triggers
// a minor bump whether the merge commit uses the git-standard message, the
// GitHub pull-request message (whose head ref is owner-prefixed), or the
// remote-tracking message (whose ref is remote-prefixed).
func TestFeatureMergeMessageFormats(t *testing.T) {
	t.Parallel()
	t.Run("GitStandard", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.checkout("develop")
		h.merge("feature/cool-abc") // "Merge branch 'feature/cool-abc'"
		h.want("0.2.0-alpha.2")     // minor bump from the feature merge
	})

	t.Run("RemoteTracking", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.checkout("develop")
		// "Merge remote-tracking branch 'origin/feature/cool-abc' into develop"
		h.mergeRemote("feature/cool-abc", "origin")
		h.want("0.2.0-alpha.2") // same minor bump, remote-tracking message
	})

	t.Run("RemoteTrackingNonFeature", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.checkout("develop")
		h.mergeRemote("bugfix/ABC-9", "origin")
		h.want("0.1.1-alpha.2") // bugfix -> patch bump only, not minor
	})

	t.Run("GithubPr", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.checkout("develop")
		// "Merge pull request #7 from acme-org/feature/cool-abc"
		h.mergePR("feature/cool-abc", 7, "acme-org")
		h.want("0.2.0-alpha.2") // same minor bump, PR-style message
	})

	t.Run("GithubPrNonFeature", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.checkout("develop")
		h.mergePR("bugfix/ABC-9", 8, "acme-org")
		h.want("0.1.1-alpha.2") // bugfix -> patch bump only, not minor
	})

	t.Run("BitbucketServer", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.checkout("develop")
		// Subject "Pull request #7: nice feature" plus body
		// "Merge in PROJECT/repo from feature/cool-abc to develop".
		h.mergeBitbucketServer("feature/cool-abc", 7, "nice feature", "PROJECT/repo", "develop")
		h.want("0.2.0-alpha.2") // same minor bump, Bitbucket Server-style message
	})

	t.Run("BitbucketServerNonFeature", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("bugfix/ABC-9")
		h.commit("b1")
		h.checkout("develop")
		h.mergeBitbucketServer("bugfix/ABC-9", 8, "a fix", "PROJECT/repo", "develop")
		h.want("0.1.1-alpha.2") // bugfix -> patch bump only, not minor
	})

	t.Run("BitbucketServerBodyWithoutSubjectNotRecognized", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.checkout("develop")
		// A "Merge in ... from feature/... to develop" body WITHOUT the
		// Bitbucket Server "Pull request #<n>:" subject must not be attributed
		// as a feature merge: it falls back to an unattributed merge (patch
		// floor), not a minor bump.
		h.mergeMsg("feature/cool-abc", "Merge in PROJECT/repo from feature/cool-abc to develop")
		h.want("0.1.1-alpha.2")
	})
}

// TestReleaseMergeMessageSemver verifies that a "+semver:" directive in the
// develop->main release-merge commit's own message is EXACT: it forces the
// release to baseCore.bump(directive), overriding whatever develop accumulated
// (it can cap the release down as well as raise it).
func TestReleaseMergeMessageSemver(t *testing.T) {
	t.Parallel()

	// "+semver: major" on the release merge lifts the release from the section's
	// patch level to major.
	t.Run("Major", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.commit("d1") // develop section: a single patch's worth of change
		h.want("0.1.1-alpha.1")
		h.checkout("main")
		h.mergeMsg("develop", "Merge branch 'develop'\n\n+semver: major")
		h.want("1.0.0") // release-merge marker raises patch -> major
	})

	// "+semver: minor" on the release merge lifts a patch-level section to minor.
	t.Run("Minor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.commit("d1")
		h.want("0.1.1-alpha.1")
		h.checkout("main")
		h.mergeMsg("develop", "Merge branch 'develop'\n\n+semver: minor")
		h.want("0.2.0") // release-merge marker raises patch -> minor
	})

	// A "+semver:" directive on the release merge is EXACT: it forces the release
	// to baseCore.bump(directive), overriding whatever develop accumulated — even
	// a develop commit's own "+semver: major". Here a "+semver: minor" release
	// merge caps a develop major down to a minor release.
	t.Run("ExactOverridesSectionBump", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.commit("rework +semver: major")
		h.want("1.0.0-alpha.1")
		h.checkout("main")
		h.mergeMsg("develop", "Merge branch 'develop'\n\n+semver: minor")
		h.want("0.2.0") // release-merge directive is exact: 0.1.0 + minor
	})
}

// TestMergeDirectiveCapsContent verifies that an explicit "+semver:" directive on
// a merge commit is EXACT — the merge is worth exactly that level, both floor
// and ceiling — so it caps whatever the merge brought in: inner "+semver:" bumps,
// feature merges, and reference-tag anchors are all overridden. In particular a
// previously-ignored "+semver: patch" merge now reliably pins a patch bump.
func TestMergeDirectiveCapsContent(t *testing.T) {
	t.Parallel()

	// develop base: main=1.2.2 synced to develop.
	setup := func(t *testing.T) *harness {
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("1.2.2", mg)
		h.checkout("develop")
		h.merge("main")
		return h
	}

	// "+semver: patch" on a merge into develop caps an inner "+semver: minor".
	t.Run("PatchCapsInnerMinorIntoDevelop", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.commit("big +semver: minor")
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: patch")
		h.want("1.2.3-alpha.5") // patch cap, inner minor ignored

		h.checkout("main")
		h.merge("develop")
		h.want("1.2.3")
	})

	// "+semver: patch" caps an inner FEATURE merge (a bugfix branch that received
	// a feature merge) when merged into develop.
	t.Run("PatchCapsInnerFeatureMerge", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/inner")
		h.commit("i1")
		h.checkout("develop")
		h.newBranch("bugfix/b")
		h.commit("b1")
		h.merge("feature/inner")
		h.checkout("develop")
		h.mergeMsg("bugfix/b", "Merge branch 'bugfix/b'\n\n+semver: patch")
		h.want("1.2.3-alpha.6") // inner feature merge capped to patch
	})

	// "+semver: minor" caps an inner "+semver: major".
	t.Run("MinorCapsInnerMajor", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.commit("break +semver: major")
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: minor")
		h.want("1.3.0-alpha.5") // major capped to minor
	})

	// "+semver: patch" caps an inner "+semver: major" (two levels down) when
	// merged into develop, and the subsequent release stays a patch.
	t.Run("PatchCapsInnerMajorIntoDevelop", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.commit("break +semver: major")
		h.want("2.0.0-x.2") // the branch itself resolves to a major before the merge
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: patch")
		h.want("1.2.3-alpha.5") // inner major capped all the way to patch

		h.checkout("main")
		h.merge("develop")
		h.want("1.2.3")
	})

	// "+semver: patch" on the merge overrides a reference-tag anchor the merged
	// branch carried.
	t.Run("PatchOverridesReferenceTag", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.tag("1.5.0-x.3", mustHead(t, h))
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: patch")
		h.want("1.2.3-alpha.4") // reference tag overridden by the patch directive
	})

	// A stronger directive in the same message wins (order-independent).
	t.Run("StrongerDirectiveInSameMessageWins", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: patch\n+semver: minor")
		h.want("1.3.0-alpha.4") // minor beats patch in the same message
	})

	// Direct merge into main: "+semver: patch" caps inner minor to a patch
	// release.
	t.Run("PatchCapsInnerMinorDirectToMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))
		h.newBranch("feature/x")
		h.commit("f1")
		h.commit("big +semver: minor")
		h.checkout("main")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: patch")
		h.want("1.2.3") // patch release, inner minor capped
	})

	// Direct merge into main: "+semver: minor" caps an inner major.
	t.Run("MinorCapsInnerMajorDirectToMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))
		h.newBranch("feature/x")
		h.commit("f1")
		h.commit("break +semver: major")
		h.checkout("main")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: minor")
		h.want("1.3.0") // minor release, inner major capped
	})

	// Direct merge into main: "+semver: patch" caps an inner "+semver: major"
	// (two levels down) to a patch release.
	t.Run("PatchCapsInnerMajorDirectToMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.commit("m1")
		h.tag("1.2.2", mustHead(t, h))
		h.newBranch("feature/x")
		h.commit("f1")
		h.commit("break +semver: major")
		h.want("2.0.0-x.2") // the branch itself resolves to a major before the merge
		h.checkout("main")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: patch")
		h.want("1.2.3") // patch release, inner major capped all the way down
	})

	// Ceilings compose through NESTED merges: the lowest cap along a path wins.
	// An outer "+semver: patch" merge introduces a subtree that itself contains
	// an inner "+semver: minor" merge over an inner "+semver: major". The inner
	// minor merge would be worth minor on its own, but the outer patch cap
	// restricts the whole introduced subtree — inner minor and inner major alike
	// — down to patch.
	t.Run("PatchCapComposesThroughNestedMinorMerge", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("inner")
		h.commit("i1")
		h.commit("boom +semver: major")
		h.checkout("develop")
		h.newBranch("outer")
		h.commit("o1")
		h.mergeMsg("inner", "Merge branch 'inner'\n\n+semver: minor")
		h.checkout("develop")
		h.mergeMsg("outer", "Merge branch 'outer'\n\n+semver: patch")
		h.want("1.2.3-alpha.7") // outer patch caps the nested minor+major subtree
	})

	// A commit reached via an INDEPENDENT, un-capped path keeps its full weight:
	// a "+semver: major" commit is merged into both branch A (under a
	// "+semver: patch" cap) and branch B (with no directive). When develop merges
	// both, B's un-capped path preserves the major even though A's path capped it.
	t.Run("IndependentUncappedPathKeepsWeight", func(t *testing.T) {
		t.Parallel()
		h := setup(t)
		h.newBranch("shared")
		h.commit("s1 +semver: major")
		h.checkout("develop")
		h.newBranch("A")
		h.commit("a1")
		h.mergeMsg("shared", "Merge branch 'shared'\n\n+semver: patch")
		h.checkout("develop")
		h.newBranch("B")
		h.commit("b1")
		h.merge("shared") // no directive: un-capped path to the shared major
		h.checkout("develop")
		h.mergeMsg("A", "Merge branch 'A'")
		h.mergeMsg("B", "Merge branch 'B'")
		h.want("2.0.0-alpha.9") // shared major survives via B's un-capped merge
	})
}

// TestBranchMergeIntoDevelopMessageSemver verifies that a "+semver:" directive
// in the merge commit's OWN message, when a topic branch is merged INTO develop
// (branch -> develop, as opposed to the develop -> main release merge), raises
// the develop section's bump. Such a merge commit is an ordinary commit inside
// develop's section, so its "+semver:" is honored by the section scan (via max
// against the per-commit bump), just like a directive on any other commit —
// without stacking an extra increment.
func TestBranchMergeIntoDevelopMessageSemver(t *testing.T) {
	t.Parallel()

	// "+semver: major" on a bugfix merge into develop lifts the section (a
	// patch's worth of change) to major.
	t.Run("BugfixMergeMajor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.newBranch("bugfix/x")
		h.commit("b1")
		h.checkout("develop")
		h.mergeMsg("bugfix/x", "Merge branch 'bugfix/x'\n\n+semver: major")
		h.want("1.0.0-alpha.2") // b1 + merge = 2 commits; merge marker raises major
	})

	// "+semver: minor" on a bugfix merge into develop lifts a patch-floor merge
	// to minor without stacking (a feature merge would already be minor).
	t.Run("BugfixMergeMinor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.newBranch("bugfix/x")
		h.commit("b1")
		h.checkout("develop")
		h.mergeMsg("bugfix/x", "Merge branch 'bugfix/x'\n\n+semver: minor")
		h.want("0.2.0-alpha.2") // merge marker raises patch -> minor
	})

	// A "+semver: major" on a feature merge into develop wins over the feature
	// merge's own minor floor (max, not stacking).
	t.Run("FeatureMergeMajorBeatsFloor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.newBranch("feature/x")
		h.commit("f1")
		h.checkout("develop")
		h.mergeMsg("feature/x", "Merge branch 'feature/x'\n\n+semver: major")
		h.want("1.0.0-alpha.2") // major from the merge message dominates minor floor
	})

	// The develop-side bump propagates to the release: merging that develop into
	// main tags the major-bumped core, confirming the branch->develop merge
	// marker is not lost across the release merge.
	t.Run("PropagatesToRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0
		h.newBranch("develop")
		h.newBranch("bugfix/x")
		h.commit("b1")
		h.checkout("develop")
		h.mergeMsg("bugfix/x", "Merge branch 'bugfix/x'\n\n+semver: major")
		h.want("1.0.0-alpha.2")
		h.checkout("main")
		h.merge("develop") // plain release merge, no marker of its own
		h.want("1.0.0")    // develop's major-bumped core is released
	})
}

// TestMainMergedIntoFeatureThenReleased covers the flow where main is merged
// INTO a feature branch that has its own commits, the feature branch is then
// merged into develop, and develop is finally released into main:
//
//	main --merge--> feature/cool (own commits) --merge--> develop --release--> main
//
// Merging main into the feature branch (a sync that pulls the latest release
// line onto the branch) must not disturb the feature branch's own minor
// increment: main here only carries patch-level work, so the feature floor keeps
// the core at the minor level and only the counter grows. The feature merge then
// lifts develop to that minor, and the release publishes it.
func TestMainMergedIntoFeatureThenReleased(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main root / 0.1.0 boundary
	h.newBranch("develop")
	h.commit("d1")

	// Cut a real release so main has advanced past the root and there is a
	// tagged boundary for everything below to build on.
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("0.1.1", mg)
	h.want("0.1.1")

	// Sync develop with the release (back-merge main into develop).
	h.checkout("develop")
	h.merge("main")
	h.want("0.1.2-alpha.2") // patch section above the 0.1.1 boundary

	// Feature branch off develop with its own commits: minor floor -> 0.2.0.
	h.newBranch("feature/cool")
	h.commit("f1")
	h.commit("f2")
	h.want("0.2.0-cool.2")

	// Merge main INTO the feature branch. main carried only patch-level work, so
	// the feature's minor floor stands: the core stays 0.2.0 and only the
	// counter advances (the merge commit counts).
	h.merge("main")
	h.want("0.2.0-cool.3")

	// A further commit on the feature branch after the main merge.
	h.commit("f3")
	h.want("0.2.0-cool.4")

	// Merge the feature branch into develop: the feature merge lifts develop to
	// the minor, and the section counter is `git rev-list 0.1.1..develop`.
	h.checkout("develop")
	h.merge("feature/cool")
	h.want("0.2.0-alpha.7")

	// Release develop into main: the minor-bumped core is published as 0.2.0.
	h.checkout("main")
	rel := h.merge("develop")
	h.want("0.2.0")
	h.tag("0.2.0", rel)
	h.want("0.2.0")
}

// TestFeatureMergePropagatesThroughBugfix verifies that a feature merge's minor
// bump is not lost when the feature branch is merged into a bugfix branch rather
// than straight into develop. The minor bump must ride along at each hop:
//
//	feature/bar --merge--> bugfix/foo --merge--> develop --release--> main
//
// The feature merge commit lives in the bugfix branch's history, then in
// develop's section, then in main's released section — so every stage sees a
// feature merge in its scanned commits and bumps minor.
func TestFeatureMergePropagatesThroughBugfix(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main root / 0.1.0 boundary
	h.newBranch("develop")

	// Bugfix branch off develop; a plain commit on it is only a patch so far.
	// The label drops the "bugfix/" type prefix, leaving "foo".
	h.newBranch("bugfix/foo")
	h.commit("b1")
	h.want("0.1.1-foo.1") // patch bump, no feature involved yet

	// Feature branch off the bugfix branch, with a commit, merged back into the
	// bugfix branch. The feature merge must lift the bugfix branch to a minor.
	h.newBranch("feature/bar")
	h.commit("f1")
	h.checkout("bugfix/foo")
	h.merge("feature/bar") // "Merge branch 'feature/bar'"
	h.want("0.2.0-foo.3")  // minor now: b1 + f1 + merge = 3 commits

	// Merge the bugfix branch into develop. The feature merge is now in develop's
	// section, so develop is minor-bumped too.
	h.checkout("develop")
	h.merge("bugfix/foo")
	h.want("0.2.0-alpha.4") // b1 + f1 + feature-merge + bugfix-merge = 4 commits

	// Release: merge develop into main and tag. The release core is develop's
	// minor-bumped core.
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("0.2.0", mg)
	h.want("0.2.0") // minor release
}

// TestFeatureMergeThroughBugfixDirectToMain verifies the same feature-merge
// minor propagation when the bugfix branch is merged STRAIGHT into main (the
// no-develop / hotfix flow) rather than through develop:
//
//	feature/bar --merge--> bugfix/foo --merge--> main
//
// The feature merge commit lives in the bugfix branch's history, so the direct
// merge into main must see it and bump minor (not the bugfix's patch floor).
func TestFeatureMergeThroughBugfixDirectToMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	// Bugfix branch off main; a plain commit is only a patch on its own.
	h.newBranch("bugfix/foo")
	h.commit("b1")
	h.want("0.1.1-foo.1") // patch bump, no feature involved yet

	// Feature branch off the bugfix branch, merged back into it: the feature
	// merge lifts the bugfix branch to a minor.
	h.newBranch("feature/bar")
	h.commit("f1")
	h.checkout("bugfix/foo")
	h.merge("feature/bar") // "Merge branch 'feature/bar'"
	h.want("0.2.0-foo.3")  // minor now: b1 + f1 + merge = 3 commits

	// Merge the bugfix branch directly into main: the feature merge in the
	// bugfix's history must lift the direct merge to a minor, applied once.
	h.checkout("main")
	h.merge("bugfix/foo")
	h.want("0.2.0") // minor release, not a patch
}

// TestMultipleMergesBumpOnce verifies that a version bump is a single-step
// increment over the release boundary's core, not a cumulative one: no matter
// how many branches are merged into a section, the core moves at most one step,
// determined by the strongest bump present. Only the develop counter (the commit
// count) grows with each merge.
func TestMultipleMergesBumpOnce(t *testing.T) {
	t.Parallel()
	// Several feature and bugfix merges into develop bump the minor exactly once
	// (the strongest bump is minor, from the feature merges), not once per merge.
	t.Run("FeaturesAndBugfixesMinorOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary
		h.newBranch("develop")

		// mergeTopic branches `name` off develop, adds one commit, and merges it
		// back into develop, returning develop to HEAD.
		mergeTopic := func(name string) {
			h.newBranch(name)
			h.commit("work on " + name)
			h.checkout("develop")
			h.merge(name)
		}

		mergeTopic("feature/a") // minor
		mergeTopic("bugfix/b")  // patch
		mergeTopic("feature/c") // minor
		mergeTopic("bugfix/d")  // patch

		// Core is 0.2.0 (minor bumped ONCE despite two feature merges), NOT 0.4.0.
		// Each topic contributes 2 commits (its own + the merge): 4 topics = 8.
		h.want("0.2.0-alpha.8")
	})

	// Several bugfix merges into develop (no feature involved) bump the patch
	// exactly once, not once per merge.
	t.Run("BugfixesPatchOnce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary
		h.newBranch("develop")

		mergeBugfix := func(name string) {
			h.newBranch(name)
			h.commit("work on " + name)
			h.checkout("develop")
			h.merge(name)
		}

		mergeBugfix("bugfix/a")
		mergeBugfix("bugfix/b")
		mergeBugfix("bugfix/c")

		// Core is 0.1.1 (patch bumped ONCE despite three bugfix merges), NOT 0.1.3.
		// Each bugfix contributes 2 commits: 3 bugfixes = 6.
		h.want("0.1.1-alpha.6")
	})
}

// TestMultipleUntaggedMergesIntoMain covers the main-based flow (NO develop
// during the merges, NO tags): several feature/bugfix branches are merged
// DIRECTLY into main, one after another. Each direct merge advances the release
// core exactly once (feature floors at minor, bugfix at patch), so the cores
// accumulate across the merges. Afterward, a develop branch and another feature
// branch derive their versions from main's accumulated core.
func TestMultipleUntaggedMergesIntoMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0, untagged

	// Merge 1: a feature branch -> minor. 0.1.0 -> 0.2.0.
	h.newBranch("feature/one")
	h.commit("f1")
	h.checkout("main")
	h.merge("feature/one")
	h.want("0.2.0")

	// Merge 2: a bugfix branch -> patch. 0.2.0 -> 0.2.1.
	h.newBranch("bugfix/two")
	h.commit("b1")
	h.checkout("main")
	h.merge("bugfix/two")
	h.want("0.2.1")

	// Merge 3: another feature branch -> minor. 0.2.1 -> 0.3.0.
	h.newBranch("feature/three")
	h.commit("f3")
	h.checkout("main")
	h.merge("feature/three")
	h.want("0.3.0") // three direct merges accumulated: minor, patch, minor

	// Downstream: a develop branch created off main. develop's reachable section
	// still includes the feature/three merge at its boundary, so the section sees
	// a feature merge and floors at minor: 0.3.0 -> 0.4.0. A develop commit only
	// advances the counter.
	h.newBranch("develop")
	h.want("0.4.0-alpha.1")
	h.commit("d1")
	h.want("0.4.0-alpha.2")

	// Downstream: another feature branch derives its minor from develop's core.
	h.newBranch("feature/x")
	h.commit("fx1")
	h.want("0.4.0-x.1")

	// The untagged direct merges left main at 0.3.0. The subsequent develop and
	// feature/x commits are downstream of main and must not change main's
	// version: re-check main and confirm it is still the accumulated 0.3.0 core.
	h.checkout("main")
	h.want("0.3.0")
}

// TestMultipleUntaggedDevelopToMainCycles covers the develop-to-main flow (WITH
// develop, NO tags): features/bugfixes are merged into develop, then develop is
// merged into main as an untagged release merge, and the whole cycle repeats.
// Each untagged develop->main release merge advances main's core once (from
// develop's accumulated section bump), so the cores accumulate across cycles.
// Afterward, develop and another feature branch derive from main's core.
func TestMultipleUntaggedDevelopToMainCycles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0, untagged
	h.newBranch("develop")

	// Cycle 1: a feature into develop, then develop -> main (untagged). The
	// release carries develop's minor: 0.1.0 -> 0.2.0.
	h.newBranch("feature/one")
	h.commit("f1")
	h.checkout("develop")
	h.merge("feature/one")
	h.checkout("main")
	h.merge("develop")
	h.want("0.2.0")

	// Cycle 2: a bugfix into develop, then develop -> main (untagged): patch.
	// 0.2.0 -> 0.2.1.
	h.checkout("develop")
	h.newBranch("bugfix/two")
	h.commit("b1")
	h.checkout("develop")
	h.merge("bugfix/two")
	h.checkout("main")
	h.merge("develop")
	h.want("0.2.1")

	// Cycle 3: another feature into develop, then develop -> main (untagged):
	// minor. 0.2.1 -> 0.3.0.
	h.checkout("develop")
	h.newBranch("feature/three")
	h.commit("f3")
	h.checkout("develop")
	h.merge("feature/three")
	h.checkout("main")
	h.merge("develop")
	h.want("0.3.0") // three untagged release merges accumulated: minor, patch, minor

	// Downstream: develop after all the cycles builds on main's 0.3.0 core.
	h.checkout("develop")
	h.want("0.3.0-alpha.2")
	h.commit("d1")
	h.want("0.3.1-alpha.1")

	// Downstream: another feature branch derives its minor from develop's core.
	h.newBranch("feature/x")
	h.commit("fx1")
	h.want("0.4.0-x.1")

	// The untagged release merges left main at 0.3.0. The subsequent develop and
	// feature/x commits are downstream of main and must not change main's
	// version: re-check main and confirm it is still the accumulated 0.3.0 core.
	h.checkout("main")
	h.want("0.3.0")
}

// TestUnattributedMergeBoundary covers develop-boundary discovery over an
// UNATTRIBUTED merge on main: a merge commit whose message matches none of the
// recognized merge forms (feature/PR/remote-tracking) defaults to a
// develop-release boundary (isDevelopReleaseMerge treats name=="" as a release
// merge). Unlike the mainVersion path (already covered), here the unattributed
// merge sits above the newest tag and a develop version is computed by walking
// main's boundaries, so developBoundaries must register that merge as the
// release boundary develop builds on.
func TestUnattributedMergeBoundary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("1.0.0", mg) // newest tag on main
	h.want("1.0.0")

	// A new develop commit, then an UNATTRIBUTED merge into main above the tag
	// (its message matches none of the merge regexes).
	h.checkout("develop")
	h.commit("d2")
	h.checkout("main")
	h.mergeMsg("develop", "integrate latest work") // unattributed -> release boundary
	// On main the unattributed merge releases develop's core: 1.0.0 -> 1.0.1.
	h.want("1.0.1")

	// develop's boundary is discovered by walking main's chain; the unattributed
	// merge registers as the 1.0.1 release boundary develop now builds on.
	h.checkout("develop")
	h.want("1.0.1-alpha.1")
	h.commit("d3")
	h.want("1.0.2-alpha.1") // d3 patches the discovered 1.0.1 boundary
}

// TestEmptyVersionLabelErrors covers a branch name whose sanitized prerelease
// label is fully empty (every character is a separator), which genver rejects
// with "produces an empty version label" rather than emitting a malformed
// prerelease.
func TestEmptyVersionLabelErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.newBranch("feature/---") // sanitizes to an empty label
	h.commit("fx")

	_, err := runCapture(t, h, "--branch", "feature/---")
	if err == nil || !strings.Contains(err.Error(), "empty version label") {
		t.Fatalf("got err %v, want it to contain %q", err, "empty version label")
	}
}

// TestOctopusMerge exercises octopus merges (a single merge commit with 3+
// parents), a real git topology that no other test builds. The version
// calculator reads only Parent(0)/Parent(1) for attribution and iterates
// parents[1:] for ceilings/anchors, so these tests pin the behavior when a
// merge brings in more than one branch at once.
func TestOctopusMerge(t *testing.T) {
	t.Parallel()

	// Octopus-merging two feature branches into develop is not a recognized
	// feature merge (the "Merge branches '...' and '...'" message matches none of
	// the merge regexes), so it contributes only a patch bump — the branches'
	// own commits and the merge commit are all counted.
	t.Run("TwoFeaturesIntoDevelop", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")
		h.newBranch("feature/a")
		h.commit("a1")
		h.commit("a2")
		h.checkout("develop")
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("develop")
		h.octopusMerge("Merge branches 'feature/a' and 'feature/b'", "feature/a", "feature/b")
		// Commits above main's 0.1.0 boundary: d1, a1, a2, b1, merge = 5.
		// The octopus message is not a recognized feature merge -> patch bump.
		h.want("0.1.1-alpha.5")
	})

	// When one octopus-merged parent carries an explicit "+semver: major" on its
	// own commit, that bump is picked up through the merge.
	t.Run("MajorOnOneParent", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")
		h.newBranch("feature/a")
		h.commit("a1 +semver: major")
		h.checkout("develop")
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("develop")
		h.octopusMerge("Merge branches 'feature/a' and 'feature/b'", "feature/a", "feature/b")
		// Commits above the boundary: d1, a1, b1, merge = 4. The major bump on a1
		// takes the core 0.1.0 -> 1.0.0.
		h.want("1.0.0-alpha.4")
	})

	// A "+semver:" directive on two DIFFERENT merged parents resolves to the
	// strongest bump across parents[1:], independent of the parent order (minor
	// on one branch, major on the other -> major).
	t.Run("SemverOnTwoParents", func(t *testing.T) {
		t.Parallel()
		build := func(aBump, bBump string) *harness {
			h := newHarness(t)
			h.commit("root") // 0.1.0 boundary on main
			h.newBranch("develop")
			h.commit("d1")
			h.newBranch("feature/a")
			h.commit("a1 +semver: " + aBump)
			h.checkout("develop")
			h.newBranch("feature/b")
			h.commit("b1 +semver: " + bBump)
			h.checkout("develop")
			h.octopusMerge("Merge branches 'feature/a' and 'feature/b'", "feature/a", "feature/b")
			return h
		}
		// Commits above the boundary: d1, a1, b1, merge = 4. Strongest = major.
		build("minor", "major").want("1.0.0-alpha.4")
		// Order-independent: major listed first still yields the same result.
		build("major", "minor").want("1.0.0-alpha.4")
	})

	// A "+semver:" directive on the octopus MERGE COMMIT's own message applies to
	// the merge, exactly like a directive on a normal merge commit.
	t.Run("SemverOnMergeCommitIntoDevelop", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")
		h.newBranch("feature/a")
		h.commit("a1")
		h.checkout("develop")
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("develop")
		h.octopusMerge("Merge branches 'feature/a' and 'feature/b' +semver: major", "feature/a", "feature/b")
		// Commits above the boundary: d1, a1, b1, merge = 4. The +semver: major on
		// the merge commit itself takes the core 0.1.0 -> 1.0.0.
		h.want("1.0.0-alpha.4")
	})

	// A "+semver: patch" on the octopus merge commit CAPS a stronger bump
	// (major) introduced by one of the merged-in parents: the merge's ceiling
	// suppresses the inner major, so only a patch lands. This exercises the
	// per-parent ceiling propagation over parents[1:].
	t.Run("PatchCapOnMergeCommitSuppressesInnerMajor", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")
		h.newBranch("feature/a")
		h.commit("a1 +semver: major") // would be major on its own
		h.checkout("develop")
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("develop")
		h.octopusMerge("Merge stuff +semver: patch", "feature/a", "feature/b")
		// Commits above the boundary: d1, a1, b1, merge = 4. The merge's
		// +semver: patch caps the inner major -> only a patch: 0.1.0 -> 0.1.1.
		h.want("0.1.1-alpha.4")

		// Releasing that develop into main yields the same patch core as a plain
		// release: the suppressed inner major must not resurface at the release.
		h.checkout("main")
		mg := h.merge("develop")
		h.want("0.1.1")    // untagged release merge on main
		h.tag("0.1.1", mg) // tagging the release is idempotent
		h.want("0.1.1")
	})

	// An octopus merge of THREE branches (four parents) counts every merged
	// branch's commits plus the single merge commit, and picks up an explicit
	// bump on ANY merged parent (here the third, beyond Parent(1)).
	t.Run("ThreeMergedBranches", func(t *testing.T) {
		t.Parallel()
		// No explicit bump: plain patch.
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")
		h.newBranch("feature/a")
		h.commit("a1")
		h.checkout("develop")
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("develop")
		h.newBranch("feature/c")
		h.commit("c1")
		h.checkout("develop")
		h.octopusMerge("Merge three", "feature/a", "feature/b", "feature/c")
		// Commits above the boundary: d1, a1, b1, c1, merge = 5. Not a recognized
		// feature merge -> patch.
		h.want("0.1.1-alpha.5")
		// Releasing into main yields the plain patch release.
		h.checkout("main")
		h.merge("develop")
		h.want("0.1.1")

		// A major on the THIRD merged branch (a parent beyond Parent(1)) is still
		// picked up, proving parents[1:] is scanned in full.
		h2 := newHarness(t)
		h2.commit("root")
		h2.newBranch("develop")
		h2.commit("d1")
		h2.newBranch("feature/a")
		h2.commit("a1")
		h2.checkout("develop")
		h2.newBranch("feature/b")
		h2.commit("b1")
		h2.checkout("develop")
		h2.newBranch("feature/c")
		h2.commit("c1 +semver: major")
		h2.checkout("develop")
		h2.octopusMerge("Merge three", "feature/a", "feature/b", "feature/c")
		// Same 5 commits; the major on the third parent takes 0.1.0 -> 1.0.0.
		h2.want("1.0.0-alpha.5")
		// Releasing into main carries the major through to the release.
		h2.checkout("main")
		h2.merge("develop")
		h2.want("1.0.0")
	})

	// An octopus merge whose message IS a recognized feature merge (the singular
	// "Merge branch 'feature/...'" form, with extra octopus parents) contributes
	// the feature minor, like any feature merge.
	t.Run("RecognizedFeatureMerge", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")
		h.newBranch("feature/main-feat")
		h.commit("mf1")
		h.checkout("develop")
		h.newBranch("helper")
		h.commit("hp1")
		h.checkout("develop")
		h.octopusMerge("Merge branch 'feature/main-feat'", "feature/main-feat", "helper")
		// Commits above the boundary: d1, mf1, hp1, merge = 4. Recognized feature
		// merge -> minor: 0.1.0 -> 0.2.0.
		h.want("0.2.0-alpha.4")
		// Releasing into main carries the feature minor through to the release.
		h.checkout("main")
		h.merge("develop")
		h.want("0.2.0")
	})

	// A prerelease reference tag on a branch tip that is integrated via an
	// octopus merge into develop anchors the section to the tag's core: the
	// merge's implicit feature-minor must NOT re-lift the anchor. integratesAnchor
	// recognizes the tag on a merged-in parent (parents[1:]) whose first-parent
	// chain reaches it, not reachable from the first parent. This is the octopus
	// analogue of TestPrereleaseReferenceTags/PropagatesToDevelop.
	t.Run("ReferenceAnchorIntegratedViaOctopus", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.checkout("main")
		mg := h.merge("develop")
		h.tag("2.1.0", mg)
		h.checkout("develop")
		h.merge("main")

		// A reference-tagged branch...
		h.newBranch("bugfix/ref")
		h.commit("b1")
		h.tag("4.5.6-foobar-x.3", mustHead(t, h))
		// ...plus a helper branch, octopus-merged together into develop.
		h.checkout("develop")
		h.newBranch("helper")
		h.commit("hp1")
		h.checkout("develop")
		h.octopusMerge("Merge branches 'bugfix/ref' and 'helper'", "bugfix/ref", "helper")
		// The tag core 4.5.6 anchors the section; alpha label; the develop section
		// count is 5 (d1, back-merge, b1, hp1, octopus-merge). The octopus's
		// feature-minor does not lift the anchor above 4.5.6.
		h.want("4.5.6-alpha.5")

		// Releasing into main yields the anchored core as a plain release.
		h.checkout("main")
		h.merge("develop")
		h.want("4.5.6")
	})

	// Nested capping octopus merges: an inner octopus with "+semver: patch" caps
	// a major introduced by one of its parents; that inner merge is then brought
	// in by an OUTER octopus carrying "+semver: minor". The inner major stays
	// suppressed (its ceiling composes through the nested merges), and the outer
	// minor is what lands.
	t.Run("NestedCappingOctopus", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // 0.1.0 boundary on main
		h.newBranch("develop")
		h.commit("d1")

		// Inner octopus on a topic branch: feature/a's major is capped to patch.
		h.newBranch("topic")
		h.newBranch("feature/a")
		h.commit("a1 +semver: major")
		h.checkout("topic")
		h.newBranch("feature/b")
		h.commit("b1")
		h.checkout("topic")
		h.octopusMerge("Inner merge +semver: patch", "feature/a", "feature/b")

		// Outer octopus into develop: brings in the capped topic plus feature/c,
		// carrying an explicit minor.
		h.checkout("develop")
		h.newBranch("feature/c")
		h.commit("c1")
		h.checkout("develop")
		h.octopusMerge("Outer merge +semver: minor", "topic", "feature/c")
		// The inner major stays capped; the outer minor lands: 0.1.0 -> 0.2.0.
		// Section commits: d1, a1, b1, inner-merge, c1, outer-merge = 6.
		h.want("0.2.0-alpha.6")

		// Releasing into main carries the minor (not the suppressed major).
		h.checkout("main")
		h.merge("develop")
		h.want("0.2.0")
	})

	// An octopus merge directly into main (a multi-hotfix flow) advances main's
	// release core exactly once, like any single merge into main.
	t.Run("IntoMainAfterRelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.release("1.0.0")
		h.checkout("main")
		h.newBranch("hotfix/x")
		h.commit("hx1")
		h.checkout("main")
		h.newBranch("hotfix/y")
		h.commit("hy1")
		h.checkout("main")
		h.octopusMerge("Merge branches 'hotfix/x' and 'hotfix/y'", "hotfix/x", "hotfix/y")
		h.want("1.0.1")
	})

	// A "+semver:" directive on an octopus merge commit landing directly on main
	// sets the release bump for that merge (minor -> 1.0.0 becomes 1.1.0).
	t.Run("SemverOnMergeCommitIntoMain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.release("1.0.0")
		h.checkout("main")
		h.newBranch("hotfix/x")
		h.commit("hx1")
		h.checkout("main")
		h.newBranch("hotfix/y")
		h.commit("hy1")
		h.checkout("main")
		h.octopusMerge("Merge branches 'hotfix/x' and 'hotfix/y' +semver: minor", "hotfix/x", "hotfix/y")
		h.want("1.1.0")
	})
}

// TestNoPermanentBranch asserts the error surfaced when a repository has neither
// a "main" nor a "master" branch. mainBranch (git.go) returns
// `no "main" or "master" branch found`, reached both when the only branch is a
// differently-named trunk and when HEAD is on a short-lived branch with no
// permanent branch present at all (the non-main entry point through
// otherVersion -> integrationBranch).
func TestNoPermanentBranch(t *testing.T) {
	t.Parallel()

	const wantErr = `no "main" or "master" branch found`

	// Only branch is "trunk": neither main nor master exists.
	t.Run("TrunkOnly", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("trunk")
		h.deleteBranch("main")
		_, err := runCapture(t, h)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("got err %v, want it to contain %q", err, wantErr)
		}
	})

	// HEAD is on a short-lived feature branch with no main/master/develop: the
	// non-main path through otherVersion -> integrationBranch -> mainBranch.
	t.Run("FeatureBranchOnly", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("f1")
		h.deleteBranch("main")
		_, err := runCapture(t, h)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("got err %v, want it to contain %q", err, wantErr)
		}
	})
}

// TestUnrelatedHistoryBranch (CASE 3) covers a short-lived branch whose history
// is entirely UNRELATED to the integration branch, while both develop and main
// exist. forkBase finds no integration-mainline ancestor of head and falls back
// to a plain merge-base, which — with disjoint histories — reports
// `no common ancestor between commits`.
func TestUnrelatedHistoryBranch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	// A feature branch with a fresh, independent root: unrelated to develop/main.
	h.orphanBranch("feature/orphan")
	h.commit("o1")
	_, err := runCapture(t, h)
	const wantErr = "no common ancestor between commits"
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("got err %v, want it to contain %q", err, wantErr)
	}
}

// TestBranchOffOrphanDevelop (CASE 1) covers a short-lived branch correctly
// forked FROM an orphan develop line that does not descend from main. forkBase
// finds the fork point normally (the branch IS related to develop), so the
// merge-base fallback is not hit; instead the develop-section scan reaches no
// release boundary and reports `no release boundary found for commit <hash>`.
// The orphan develop line itself produces the same error.
func TestBranchOffOrphanDevelop(t *testing.T) {
	t.Parallel()
	const wantErr = "no release boundary found for commit"

	h := newHarness(t)
	h.commit("root") // main's history
	h.commit("m1")
	// develop is an independent line with its own root, not forked from main.
	h.orphanBranch("develop")
	h.commit("od1")
	h.commit("od2")

	// develop itself has no reachable release boundary.
	_, derr := runCapture(t, h)
	if derr == nil || !strings.Contains(derr.Error(), wantErr) {
		t.Fatalf("orphan develop: got err %v, want it to contain %q", derr, wantErr)
	}

	// A feature branch correctly forked off the orphan develop hits the same
	// section-scan error (the fork point is found, but the section below it
	// reaches no boundary).
	h.newBranch("feature/x")
	h.commit("fx1")
	_, ferr := runCapture(t, h)
	if ferr == nil || !strings.Contains(ferr.Error(), wantErr) {
		t.Fatalf("branch off orphan develop: got err %v, want it to contain %q", ferr, wantErr)
	}
}
