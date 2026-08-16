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
		h.deleteBranch("bugfix/rogue")
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
		h.deleteBranch("bugfix/ref")

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
		h.deleteBranch("bugfix/ref")

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

	// Same direct-into-main flow with a feature branch: the reference core still
	// governs the release even though a feature merge would bump minor.
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
		h.want("4.5.6")
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
