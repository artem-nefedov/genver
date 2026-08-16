package main

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// When no --format / --tag-format is given, givi guesses the default spelling
// from the boundary tag the version builds on: a "v"-prefixed boundary tag
// yields "v"-prefixed output and tags, a bare (or absent) one stays bare.
// Explicit templates always win.

// TestVPrefixInheritedOnMain: a "v"-prefixed release tag on main makes the next
// computed version print (and tag) with a leading "v" by default.
func TestVPrefixInheritedOnMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	c := h.commit("r1")
	h.tag("v0.1.1", c) // v-prefixed boundary tag at HEAD
	head := h.commit("r2")

	// HEAD is one patch above v0.1.1 -> 0.1.2, and the "v" is inherited.
	out, err := runCapture(t, h, "--tag-main")
	if err != nil || out != "v0.1.2" {
		t.Fatalf("main v-inherit: out=%q err=%v, want %q", out, err, "v0.1.2")
	}
	// The default tag inherits the "v" too.
	if got := localTagHash(t, h, "v0.1.2"); got != head {
		t.Errorf("tag v0.1.2 points at %s, want HEAD %s", got, head)
	}
	if got := localTagHash(t, h, "0.1.2"); got != plumbing.ZeroHash {
		t.Errorf("bare tag 0.1.2 exists (%s) but the v-prefixed boundary should shape it", got)
	}
}

// TestBarePrefixInheritedOnMain: a bare release tag on main keeps the output
// and tag bare by default.
func TestBarePrefixInheritedOnMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	c := h.commit("r1")
	h.tag("0.1.1", c) // bare boundary tag
	head := h.commit("r2")

	out, err := runCapture(t, h, "--tag-main")
	if err != nil || out != "0.1.2" {
		t.Fatalf("main bare-inherit: out=%q err=%v, want %q", out, err, "0.1.2")
	}
	if got := localTagHash(t, h, "0.1.2"); got != head {
		t.Errorf("tag 0.1.2 points at %s, want HEAD %s", got, head)
	}
	if got := localTagHash(t, h, "v0.1.2"); got != plumbing.ZeroHash {
		t.Errorf("v-prefixed tag v0.1.2 exists (%s) but a bare boundary should stay bare", got)
	}
}

// TestNoBoundaryTagStaysBare: with no usable tag anywhere (fresh repo, implicit
// root boundary), the default stays bare.
func TestNoBoundaryTagStaysBare(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0, no tags

	out, err := runCapture(t, h)
	if err != nil || out != "0.1.0" {
		t.Fatalf("no-boundary: out=%q err=%v, want %q", out, err, "0.1.0")
	}
}

// TestMixedSpellingPrefersV: when both "v0.1.1" and "0.1.1" tag the same
// boundary commit, the "v" spelling wins, so the default is "v"-prefixed.
func TestMixedSpellingPrefersV(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	c := h.commit("r1")
	h.tag("v0.1.1", c)
	h.tag("0.1.1", c) // same version; "v" wins
	h.commit("r2")

	out, err := runCapture(t, h)
	if err != nil || out != "v0.1.2" {
		t.Fatalf("mixed-spelling: out=%q err=%v, want v-prefixed %q", out, err, "v0.1.2")
	}
}

// TestExplicitFormatOverridesInheritedV: an explicit --format always wins over
// the inherited "v" default for stdout and --write-to; the tag still inherits
// the "v" when --tag-format is absent.
func TestExplicitFormatOverridesInheritedV(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	c := h.commit("r1")
	h.tag("v0.1.1", c)
	head := h.commit("r2")

	out, err := runCapture(t, h, "--tag-main", "--format", "{{.Full}}", "--write-to", "version.txt")
	if err != nil || out != "0.1.2" {
		t.Fatalf("explicit --format: out=%q err=%v, want bare %q", out, err, "0.1.2")
	}
	if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "0.1.2" {
		t.Errorf("write-to content = %q, want bare %q", got, "0.1.2")
	}
	// The tag still inherits the "v" (no --tag-format given).
	if got := localTagHash(t, h, "v0.1.2"); got != head {
		t.Errorf("tag v0.1.2 points at %s, want HEAD %s", got, head)
	}
}

// TestExplicitTagFormatOverridesInheritedV: an explicit --tag-format wins over
// the inherited "v" default for the tag, while stdout keeps the inherited "v".
func TestExplicitTagFormatOverridesInheritedV(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	c := h.commit("r1")
	h.tag("v0.1.1", c)
	head := h.commit("r2")

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "rel-{{.Full}}")
	if err != nil || out != "v0.1.2" {
		t.Fatalf("explicit --tag-format: out=%q err=%v, want inherited %q", out, err, "v0.1.2")
	}
	// The tag follows --tag-format, not the inherited default.
	if got := localTagHash(t, h, "rel-0.1.2"); got != head {
		t.Errorf("tag rel-0.1.2 points at %s, want HEAD %s", got, head)
	}
	if got := localTagHash(t, h, "v0.1.2"); got != plumbing.ZeroHash {
		t.Errorf("inherited tag v0.1.2 exists (%s) but --tag-format should shape it", got)
	}
}

// TestVPrefixInheritedOnDevelop: the "v" inherited from a boundary tag also
// applies to a develop prerelease version by default.
func TestVPrefixInheritedOnDevelop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	h.newBranch("develop")
	h.commit("d1")
	h.release("v0.1.1") // v-prefixed release tag on main
	h.backMerge()
	h.commit("d2")

	out, err := runCapture(t, h)
	if err != nil {
		t.Fatalf("develop v-inherit: err=%v", err)
	}
	if !strings.HasPrefix(out, "v") {
		t.Errorf("develop stdout %q should inherit the v prefix from the boundary tag", out)
	}
}

// The "v" default is driven solely by the release BOUNDARY tag, never by a
// prerelease reference tag. A reference tag's own spelling is ignored — even
// when that reference tag's core wins and shapes the version number.

// TestReferenceTagSpellingIgnoredBoundaryV: a "v"-prefixed boundary tag with a
// bare reference tag that wins the version -> the version number comes from the
// (bare) reference tag, but the "v" is inherited from the boundary.
func TestReferenceTagSpellingIgnoredBoundaryV(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("v2.1.0", mg) // v-prefixed release boundary
	h.checkout("develop")
	h.merge("main")

	h.newBranch("bugfix/ref")
	h.commit("b1")
	h.tag("4.5.6-foobar-x.3", mustHead(t, h)) // bare reference tag; core wins over 2.1.x

	out, err := runCapture(t, h, "--branch", "bugfix/ref")
	if err != nil {
		t.Fatalf("boundary-v ref-bare: err=%v", err)
	}
	// The reference tag's core wins the version number...
	if !strings.Contains(out, "4.5.6-foobar-x.3") {
		t.Errorf("stdout %q should carry the reference tag's version 4.5.6-foobar-x.3", out)
	}
	// ...but the "v" comes from the boundary tag, not the bare reference tag.
	if out != "v4.5.6-foobar-x.3" {
		t.Errorf("stdout %q: expected the v inherited from the v-prefixed boundary", out)
	}
}

// TestReferenceTagSpellingIgnoredBoundaryBare: a bare boundary tag with a
// "v"-prefixed reference tag that wins -> the version stays bare; the reference
// tag's "v" spelling is ignored.
func TestReferenceTagSpellingIgnoredBoundaryBare(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("2.1.0", mg) // bare release boundary
	h.checkout("develop")
	h.merge("main")

	h.newBranch("bugfix/ref")
	h.commit("b1")
	h.tag("v4.5.6-foobar-x.3", mustHead(t, h)) // v-prefixed reference tag; core wins

	out, err := runCapture(t, h, "--branch", "bugfix/ref")
	if err != nil {
		t.Fatalf("boundary-bare ref-v: err=%v", err)
	}
	if strings.HasPrefix(out, "v") {
		t.Errorf("stdout %q must stay bare: the reference tag's v spelling is ignored", out)
	}
	if out != "4.5.6-foobar-x.3" {
		t.Errorf("stdout %q: expected bare 4.5.6-foobar-x.3", out)
	}
}

// TestReferenceTagVerbatimUsesBoundarySpelling: on the very commit a "v"-prefixed
// reference tag marks, the version equals the tag verbatim, yet the "v" is still
// sourced from the (bare) boundary — the reference tag's own "v" is ignored, so
// the output stays bare.
func TestReferenceTagVerbatimUsesBoundarySpelling(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("2.1.0", mg) // bare release boundary
	h.checkout("develop")
	h.merge("main")

	h.newBranch("bugfix/ref")
	h.commit("b1")
	h.tag("v4.5.6-foobar-x.3", mustHead(t, h)) // v-prefixed reference tag, verbatim here

	out, err := runCapture(t, h, "--branch", "bugfix/ref")
	if err != nil {
		t.Fatalf("verbatim ref: err=%v", err)
	}
	if out != "4.5.6-foobar-x.3" {
		t.Errorf("stdout %q: reference-tag spelling must be ignored; expected bare 4.5.6-foobar-x.3", out)
	}
}
