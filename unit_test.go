package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

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
		// Multiple directives in one message: the strongest wins, regardless of
		// the order they appear in.
		"+semver:minor +semver:major": bumpMajor,
		"+semver:major +semver:minor": bumpMajor,
		"+semver:patch +semver:minor": bumpMinor,
		"+semver:minor +semver:patch": bumpMinor,
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

// --- run()/flag behavior tests ---

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
			"{{.Full}}":                          "0.1.1-alpha.2",
			"{{.Core}}":                          "0.1.1",
			"{{.PreRelease}}":                    "-alpha.2",
			"{{.Count}}":                         "2",
			"{{.Major}}.{{.Minor}}.{{.Patch}}":   "0.1.1",
			"{{.Core}}{{.PreRelease}}":           "0.1.1-alpha.2",
			"v{{.Core}}":                         "v0.1.1",
			"major={{.Major}} minor={{.Minor}}":  "major=0 minor=1",
			"image:{{.Core}}{{.PreRelease}}-dbg": "image:0.1.1-alpha.2-dbg",
			"build.{{.Count}}":                   "build.2",
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

		if out, err := runCapture(t, h, "--format", "{{.PreRelease}}"); err != nil || out != "-cool-abc.3" {
			t.Errorf("prerelease on feature: out=%q err=%v", out, err)
		}
		if out, err := runCapture(t, h, "--format", "{{.Count}}"); err != nil || out != "3" {
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
		if out, err := runCapture(t, h, "--format", "{{.Branch}}"); err != nil || out != "main" {
			t.Errorf("branch on main: out=%q err=%v", out, err)
		}

		// develop
		h.newBranch("develop")
		if out, err := runCapture(t, h, "--format", "{{.Branch}}"); err != nil || out != "develop" {
			t.Errorf("branch on develop: out=%q err=%v", out, err)
		}

		// feature branch: the exact "feature/cool-abc", not the sanitized label.
		h.newBranch("feature/cool-abc")
		h.commit("f1")
		if out, err := runCapture(t, h, "--format", "{{.Branch}}"); err != nil || out != "feature/cool-abc" {
			t.Errorf("branch on feature: out=%q err=%v", out, err)
		}
		// Combined with the version, as one might tag an image.
		if out, err := runCapture(t, h, "--format", "{{.Branch}}@{{.Full}}"); err != nil || out != "feature/cool-abc@0.2.0-cool-abc.1" {
			t.Errorf("branch+full: out=%q err=%v", out, err)
		}
	})

	// HeadHash exposes the full 40-char HEAD commit hash. The short hash is
	// obtained in-template with Sprig's substr, e.g. {{substr 0 8 .HeadHash}}.
	// Both must equal the actual HEAD hash of the repo the version was computed
	// for.
	t.Run("ShaVariables", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		head := h.commit("d1")
		want := head.String()

		if out, err := runCapture(t, h, "--format", "{{.HeadHash}}"); err != nil || out != want {
			t.Errorf("hash: out=%q err=%v, want %q", out, err, want)
		}
		// Sprig's substr abbreviates the hash: {{substr 0 8 .HeadHash}} is the
		// 8-char short hash.
		if out, err := runCapture(t, h, "--format", "{{substr 0 8 .HeadHash}}"); err != nil || out != want[:8] {
			t.Errorf("short hash via substr: out=%q err=%v, want %q", out, err, want[:8])
		}
		// A realistic image tag combining version and short hash.
		if out, err := runCapture(t, h, "--format", "{{.Core}}-{{substr 0 8 .HeadHash}}"); err != nil || out != "0.1.1-"+want[:8] {
			t.Errorf("core+short hash: out=%q err=%v, want %q", out, err, "0.1.1-"+want[:8])
		}
	})

	// A release version (on main) has an empty prerelease tail.
	t.Run("MainReleaseEmptyPrerelease", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		if out, err := runCapture(t, h, "--format", "{{.Full}}"); err != nil || out != "0.1.0" {
			t.Errorf("full on main: out=%q err=%v", out, err)
		}
		// The prerelease tail is empty on a release, so core and full match and a
		// bare {{.PreRelease}} renders nothing.
		if out, err := runCapture(t, h, "--format", "{{.Core}}{{.PreRelease}}"); err != nil || out != "0.1.0" {
			t.Errorf("core+prerelease on release: out=%q err=%v", out, err)
		}
		if out, err := runCapture(t, h, "--format", "x{{.PreRelease}}y"); err != nil || out != "xy" {
			t.Errorf("empty prerelease: out=%q err=%v", out, err)
		}
		// The count is likewise empty on a release version.
		if out, err := runCapture(t, h, "--format", "x{{.Count}}y"); err != nil || out != "xy" {
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

		sep, err := runCapture(t, h, "--format", "{{.Core}}")
		if err != nil || sep != "0.1.1" {
			t.Errorf("--format arg: out=%q err=%v", sep, err)
		}
		eq, err := runCapture(t, h, "--format={{.Core}}")
		if err != nil || eq != "0.1.1" {
			t.Errorf("--format=arg: out=%q err=%v", eq, err)
		}
	})

	// Go template features work on our variables: pipelines, built-in functions
	// like printf, and — because Major/Minor/Patch are integers, not strings —
	// integer arithmetic and numeric formatting.
	t.Run("TemplateFeatures", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")
		h.commit("d2") // develop = 0.1.1-alpha.2

		cases := map[string]string{
			`{{.Core}}`:                                "0.1.1",
			`{{printf "%s+%s" .Core .Count}}`:          "0.1.1+2",
			`{{printf "%02d" .Minor}}`:                 "01", // numeric formatting: Minor is an int
			`{{printf "%d" .Patch}}`:                   "1",  // Patch is an int
			`{{if .PreRelease}}pre{{else}}rel{{end}}`:  "pre",
			`{{trimPrefix "-" .PreRelease}}`:           "alpha.2", // strip the leading dash
			`{{.Core}}+{{trimPrefix "-" .PreRelease}}`: "0.1.1+alpha.2",
		}
		for tmpl, want := range cases {
			out, err := runCapture(t, h, "--format", tmpl)
			if err != nil || out != want {
				t.Errorf("--format %q: out=%q err=%v, want %q", tmpl, out, err, want)
			}
		}
	})

	// The integer variables (Major, Minor, Patch) are passed as integers, so
	// template arithmetic on them works without any string parsing.
	t.Run("IntegerArithmetic", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1") // develop = 0.1.1-alpha.1, core minor=1 patch=1

		// The Go template "add" isn't built in, but printf with a computed width
		// and comparison operators (eq/lt) treat these as numbers, not strings.
		if out, err := runCapture(t, h, "--format", `{{if eq .Minor 1}}minor-is-one{{end}}`); err != nil || out != "minor-is-one" {
			t.Errorf("eq on int Minor: out=%q err=%v", out, err)
		}
		if out, err := runCapture(t, h, "--format", `{{if lt .Major 5}}small{{end}}`); err != nil || out != "small" {
			t.Errorf("lt on int Major: out=%q err=%v", out, err)
		}
	})

	// By default only Sprig's hermetic functions are available: a hermetic
	// function like substr works, but a non-hermetic one like now is unknown and
	// errors. With --allow-nonhermetic the full Sprig set is exposed, so now
	// resolves.
	t.Run("HermeticFunctions", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1") // develop = 0.1.1-alpha.1

		// A hermetic function (substr) works with no extra flag.
		if out, err := runCapture(t, h, "--format", `{{substr 0 3 .Core}}`); err != nil || out != "0.1" {
			t.Errorf("hermetic substr: out=%q err=%v, want %q", out, err, "0.1")
		}

		// Non-hermetic functions (now, env, uuidv4) are not registered by
		// default, so the template fails to parse.
		for _, tmpl := range []string{`{{now}}`, `{{env "PATH"}}`, `{{uuidv4}}`} {
			if out, err := runCapture(t, h, "--format", tmpl); err == nil {
				t.Errorf("non-hermetic %q should error without --allow-nonhermetic, got out=%q", tmpl, out)
			}
		}

		// With --allow-nonhermetic the same non-hermetic functions resolve
		// (they parse and execute without error); we only assert they no longer
		// error, since their output is not deterministic.
		for _, tmpl := range []string{`{{now | date "2006"}}`, `{{uuidv4 | len}}`} {
			if _, err := runCapture(t, h, "--allow-nonhermetic", "--format", tmpl); err != nil {
				t.Errorf("non-hermetic %q with --allow-nonhermetic should succeed: err=%v", tmpl, err)
			}
		}
		// substr still works in non-hermetic mode too.
		if out, err := runCapture(t, h, "--allow-nonhermetic", "--format", `{{substr 0 3 .Core}}`); err != nil || out != "0.1" {
			t.Errorf("hermetic substr in non-hermetic mode: out=%q err=%v, want %q", out, err, "0.1")
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

		if out, err := runCapture(t, h, "--format", "{{.Nope}}"); err == nil {
			t.Errorf("unknown variable should error, got out=%q", out)
		}
		// A typo of a real variable must also fail rather than expand to "".
		if out, err := runCapture(t, h, "--format", "{{.Prerlease}}"); err == nil {
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
