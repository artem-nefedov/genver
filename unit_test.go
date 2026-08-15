package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestSanitizeLabel(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"bugfix/ABC-123-foo_bar": "ABC-123-foo-bar",
		"feature/cool-abc":       "cool-abc",
		"feature/cool_xyz":       "cool-xyz",
		"ABC-456":                "ABC-456",
		"hotfix/---weird--":      "weird",
		"a/b/c":                  "c",
	}
	for in, want := range cases {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBumpFromMessage(t *testing.T) {
	t.Parallel()
	cases := map[string]bumpKind{
		"regular commit":                bumpNone,
		"fix things +semver: minor":     bumpMinor,
		"big change +semver: major now": bumpMajor,
		"+semver:patch":                 bumpPatch,
		"+semver: MINOR":                bumpMinor,
	}
	for msg, want := range cases {
		if got := bumpFromMessage(msg); got != want {
			t.Errorf("bumpFromMessage(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestBumpResetsLowerComponents(t *testing.T) {
	t.Parallel()
	c := core{2, 3, 4}
	if got := c.bump(bumpMajor); got != (core{3, 0, 0}) {
		t.Errorf("major bump = %v, want 3.0.0", got)
	}
	if got := c.bump(bumpMinor); got != (core{2, 4, 0}) {
		t.Errorf("minor bump = %v, want 2.4.0", got)
	}
	if got := c.bump(bumpPatch); got != (core{2, 3, 5}) {
		t.Errorf("patch bump = %v, want 2.3.5", got)
	}
	if got := c.bump(bumpNone); got != c {
		t.Errorf("none bump = %v, want unchanged", got)
	}
}

func TestFormatRejectsBuildMetadataAndValidatesDockerTag(t *testing.T) {
	t.Parallel()
	// Valid release and prerelease.
	if v, err := format(core{1, 2, 3}, ""); err != nil || v != "1.2.3" {
		t.Errorf("format release = %q, %v", v, err)
	}
	if v, err := format(core{1, 2, 3}, "alpha.4"); err != nil || v != "1.2.3-alpha.4" {
		t.Errorf("format prerelease = %q, %v", v, err)
	}
	// Build metadata (illegal docker tag char '+') must be rejected.
	if _, err := format(core{1, 2, 3}, "alpha.4+build"); err == nil {
		t.Error("format with build metadata should error")
	}
	// The final string must fit the docker tag grammar.
	if v, _ := format(core{1, 2, 3}, "ABC-123-foo-bar.0"); v != "1.2.3-ABC-123-foo-bar.0" {
		t.Errorf("format docker-safe prerelease = %q", v)
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
		h.deleteBranch("feature/x")
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
		h.deleteBranch("feature/cool")
		h.want("2.2.0")
	})
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
		h.deleteBranch("feature/x")
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
		h.deleteBranch("feature/next")
		h.want("1.3.0") // minor bump off the annotated 1.2.0 release, once
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
		h.deleteBranch("feature/cool-abc")
		h.want("0.2.0") // minor bumped ONCE (not once per feature commit)
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
		h.deleteBranch("feat/cool-abc")
		h.want("0.2.0") // minor bumped ONCE via the feat/ merge
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
		h.deleteBranch("feature/cool-abc")
		h.want("0.2.0") // minor bumped ONCE (not once per feature commit)
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
		h.deleteBranch("bugfix/ABC-9")
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
		h.deleteBranch("bugfix/ABC-9")
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
		h.deleteBranch("bugfix/ABC-9")
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
		h.deleteBranch("feature/big")
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
		h.deleteBranch("hotfix/urgent")
		h.want("0.2.0") // minor from the merged commit, applied once
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
		h.deleteBranch("feature/next")
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
		h.deleteBranch("bugfix/ABC-9")
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
		h.deleteBranch("bugfix/ABC-9")
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
// what givi would otherwise calculate (whether the tag is higher OR lower than
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
	// calculation (which would reach 2.0.0) is overridden by the 0.3.0 tag.
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

		// develop off the tagged commit builds on 0.3.0.
		h.checkout("main")
		h.newBranch("develop")
		h.want("0.3.1-alpha.1") // one commit ("plain") above the 0.3.0 boundary
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
		h.deleteBranch("feature/cool-abc")
		h.want("0.2.0-alpha.2") // minor bump from the feature merge
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
		h.deleteBranch("feature/cool-abc")
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
		h.deleteBranch("bugfix/ABC-9")
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
		h.deleteBranch("feature/cool-abc")
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
		h.deleteBranch("bugfix/ABC-9")
		h.want("0.1.1-alpha.2") // bugfix -> patch bump only, not minor
	})
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
	h.deleteBranch("feature/bar")
	h.want("0.2.0-foo.3") // minor now: b1 + f1 + merge = 3 commits

	// Merge the bugfix branch into develop. The feature merge is now in develop's
	// section, so develop is minor-bumped too.
	h.checkout("develop")
	h.merge("bugfix/foo")
	h.deleteBranch("bugfix/foo")
	h.want("0.2.0-alpha.4") // b1 + f1 + feature-merge + bugfix-merge = 4 commits

	// Release: merge develop into main and tag. The release core is develop's
	// minor-bumped core.
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("0.2.0", mg)
	h.want("0.2.0") // minor release
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
			h.deleteBranch(name)
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
			h.deleteBranch(name)
		}

		mergeBugfix("bugfix/a")
		mergeBugfix("bugfix/b")
		mergeBugfix("bugfix/c")

		// Core is 0.1.1 (patch bumped ONCE despite three bugfix merges), NOT 0.1.3.
		// Each bugfix contributes 2 commits: 3 bugfixes = 6.
		h.want("0.1.1-alpha.6")
	})
}

// --- run()/flag behavior tests ---

func TestFlagHelpAndVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	out, err := runCapture(t, h, "--help")
	if err != nil || !strings.Contains(out, "Usage: givi") {
		t.Errorf("--help: out=%q err=%v", out, err)
	}
	// --help beats --version.
	out, err = runCapture(t, h, "--help", "--version")
	if err != nil || !strings.Contains(out, "Usage: givi") {
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

// TestFormat covers the --format template: each supported variable, the
// prerelease tail's leading dash (and its emptiness on a release), the
// both-syntaxes rule ("--format arg" and "--format=arg"), and strict-mode
// failure on an unknown variable.
func TestFormat(t *testing.T) {
	t.Parallel()
	// A develop version has a prerelease tail: 0.1.2-alpha.2 (two develop commits
	// so the count is a distinct value, not 1).
	t.Run("DevelopPrerelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.commit("d2")

		cases := map[string]string{
			"${full}":                        "0.1.1-alpha.2",
			"${core}":                        "0.1.1",
			"${prerelease}":                  "-alpha.2",
			"${count}":                       "2",
			"${major}.${minor}.${patch}":     "0.1.1",
			"${core}${prerelease}":           "0.1.1-alpha.2",
			"v${core}":                       "v0.1.1",
			"major=${major} minor=${minor}":  "major=0 minor=1",
			"image:${core}${prerelease}-dbg": "image:0.1.1-alpha.2-dbg",
			"build.${count}":                 "build.2",
		}
		for tmpl, want := range cases {
			out, err := runCapture(t, h, "--format", tmpl)
			if err != nil || out != want {
				t.Errorf("--format %q: out=%q err=%v, want %q", tmpl, out, err, want)
			}
		}
	})

	// On a non-develop branch the label differs but the count is still the
	// trailing counter after the last dot.
	t.Run("OtherBranchIncrement", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		h.commit("f2")
		h.commit("f3")

		if out, err := runCapture(t, h, "--format", "${prerelease}"); err != nil || out != "-cool-abc.3" {
			t.Errorf("prerelease on feature: out=%q err=%v", out, err)
		}
		if out, err := runCapture(t, h, "--format", "${count}"); err != nil || out != "3" {
			t.Errorf("count on feature: out=%q err=%v", out, err)
		}
	})

	// The branch variable is the exact branch name — unsanitized, including any
	// "/" — regardless of which branch class computed the version.
	t.Run("BranchExactName", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")

		// main
		if out, err := runCapture(t, h, "--format", "${branch}"); err != nil || out != "main" {
			t.Errorf("branch on main: out=%q err=%v", out, err)
		}

		// develop
		h.newBranch("develop")
		if out, err := runCapture(t, h, "--format", "${branch}"); err != nil || out != "develop" {
			t.Errorf("branch on develop: out=%q err=%v", out, err)
		}

		// feature branch: the exact "feature/cool-abc", not the sanitized label.
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		if out, err := runCapture(t, h, "--format", "${branch}"); err != nil || out != "feature/cool-abc" {
			t.Errorf("branch on feature: out=%q err=%v", out, err)
		}
		// Combined with the version, as one might tag an image.
		if out, err := runCapture(t, h, "--format", "${branch}@${full}"); err != nil || out != "feature/cool-abc@0.2.0-cool-abc.1" {
			t.Errorf("branch+full: out=%q err=%v", out, err)
		}
	})

	// shortsha and longsha expose the HEAD commit hash: longsha is the full
	// 40-char hex, shortsha its 8-char abbreviation. Both must equal the actual
	// HEAD hash of the repo the version was computed for.
	t.Run("ShaVariables", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		head := h.commit("d1")
		want := head.String()

		if out, err := runCapture(t, h, "--format", "${longsha}"); err != nil || out != want {
			t.Errorf("longsha: out=%q err=%v, want %q", out, err, want)
		}
		if out, err := runCapture(t, h, "--format", "${shortsha}"); err != nil || out != want[:8] {
			t.Errorf("shortsha: out=%q err=%v, want %q", out, err, want[:8])
		}
		// A realistic image tag combining version and short sha.
		if out, err := runCapture(t, h, "--format", "${core}-${shortsha}"); err != nil || out != "0.1.1-"+want[:8] {
			t.Errorf("core+shortsha: out=%q err=%v, want %q", out, err, "0.1.1-"+want[:8])
		}
	})

	// A release version (on main) has an empty prerelease tail.
	t.Run("MainReleaseEmptyPrerelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		if out, err := runCapture(t, h, "--format", "${full}"); err != nil || out != "0.1.0" {
			t.Errorf("full on main: out=%q err=%v", out, err)
		}
		// The prerelease tail is empty on a release, so core and full match and a
		// bare ${prerelease} renders nothing.
		if out, err := runCapture(t, h, "--format", "${core}${prerelease}"); err != nil || out != "0.1.0" {
			t.Errorf("core+prerelease on release: out=%q err=%v", out, err)
		}
		if out, err := runCapture(t, h, "--format", "x${prerelease}y"); err != nil || out != "xy" {
			t.Errorf("empty prerelease: out=%q err=%v", out, err)
		}
		// The count is likewise empty on a release version.
		if out, err := runCapture(t, h, "--format", "x${count}y"); err != nil || out != "xy" {
			t.Errorf("empty count on release: out=%q err=%v", out, err)
		}
	})

	// Both "--format arg" and "--format=arg" must work.
	t.Run("BothSyntaxes", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")

		sep, err := runCapture(t, h, "--format", "${core}")
		if err != nil || sep != "0.1.1" {
			t.Errorf("--format arg: out=%q err=%v", sep, err)
		}
		eq, err := runCapture(t, h, "--format=${core}")
		if err != nil || eq != "0.1.1" {
			t.Errorf("--format=arg: out=%q err=%v", eq, err)
		}
	})

	// The envsubst library's bash-style parameter expansion operators work on
	// our variables — e.g. ${prerelease#-} strips the leading dash. This is the
	// idiomatic way to get the prerelease tail without its dash.
	t.Run("ParameterExpansion", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.commit("d2") // develop = 0.1.1-alpha.2

		cases := map[string]string{
			"${prerelease#-}":   "alpha.2", // strip the leading dash
			"${prerelease##*.}": "2",       // everything after the last dot
			"${prerelease#*.}":  "2",       // everything after the first dot
			"${full%-*}":        "0.1.1",   // drop the trailing "-<tail>"
			"${full/-/+}":       "0.1.1+alpha.2",
			"${core//./_}":      "0_1_1", // replace all dots
		}
		for tmpl, want := range cases {
			out, err := runCapture(t, h, "--format", tmpl)
			if err != nil || out != want {
				t.Errorf("--format %q: out=%q err=%v, want %q", tmpl, out, err, want)
			}
		}
	})

	// Strict mode: referencing an unknown variable is an error, not a silent
	// empty substitution.
	t.Run("StrictUnknownVariable", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")

		if out, err := runCapture(t, h, "--format", "${nope}"); err == nil {
			t.Errorf("unknown variable should error, got out=%q", out)
		}
		// A typo of a real variable must also fail rather than expand to "".
		if out, err := runCapture(t, h, "--format", "${prerlease}"); err == nil {
			t.Errorf("typo'd variable should error, got out=%q", out)
		}
	})
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
