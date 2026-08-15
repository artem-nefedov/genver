package main

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// localTagHash returns the hash the named tag points at in the harness's
// repository, or the zero hash if the tag does not exist.
func localTagHash(t *testing.T, h *harness, tag string) plumbing.Hash {
	t.Helper()
	ref, err := h.g.r.Reference(plumbing.NewTagReferenceName(tag), false)
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

// TestTagFormatOnly: with only --tag-format, it shapes stdout exactly like
// --format AND shapes the tag created by --tag-main.
func TestTagFormatOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "v${full}")
	if err != nil || out != "v0.1.0" {
		t.Fatalf("--tag-format only: out=%q err=%v, want stdout %q", out, err, "v0.1.0")
	}

	// The tag must use the --tag-format template too.
	if got := localTagHash(t, h, "v0.1.0"); got != head {
		t.Errorf("tag v0.1.0 points at %s, want HEAD %s", got, head)
	}
	// The unformatted full version must NOT have been tagged.
	if got := localTagHash(t, h, "0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("unformatted tag 0.1.0 exists (%s) but --tag-format should shape the tag", got)
	}
}

// TestFormatOnlyLeavesTagUnshaped: --format alone shapes stdout but the tag
// remains the full computed version.
func TestFormatOnlyLeavesTagUnshaped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main", "--format", "v${full}")
	if err != nil || out != "v0.1.0" {
		t.Fatalf("--format only: out=%q err=%v, want stdout %q", out, err, "v0.1.0")
	}

	// The tag is the full version, unaffected by --format.
	if got := localTagHash(t, h, "0.1.0"); got != head {
		t.Errorf("tag 0.1.0 points at %s, want HEAD %s", got, head)
	}
	if got := localTagHash(t, h, "v0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("formatted tag v0.1.0 exists (%s) but --format must not shape the tag", got)
	}
}

// TestFormatAndTagFormat: when both are given, --format shapes stdout (and
// --write-to) while --tag-format shapes only the tag.
func TestFormatAndTagFormat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h,
		"--tag-main",
		"--format", "out-${full}",
		"--tag-format", "tag-${full}",
		"--write-to", "version.txt",
	)
	if err != nil || out != "out-0.1.0" {
		t.Fatalf("both formats: out=%q err=%v, want stdout %q", out, err, "out-0.1.0")
	}

	// --write-to follows --format, not --tag-format.
	if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "out-0.1.0" {
		t.Errorf("write-to content = %q, want %q", got, "out-0.1.0")
	}

	// The tag follows --tag-format.
	if got := localTagHash(t, h, "tag-0.1.0"); got != head {
		t.Errorf("tag tag-0.1.0 points at %s, want HEAD %s", got, head)
	}
	if got := localTagHash(t, h, "out-0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("stdout-format tag out-0.1.0 exists (%s) but the tag must use --tag-format", got)
	}
}

// TestTagFormatWriteTo: with only --tag-format, --write-to gets the same shaped
// output as stdout.
func TestTagFormatWriteTo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-format", "v${full}", "--write-to", "version.txt")
	if err != nil || out != "v0.1.0" {
		t.Fatalf("--tag-format --write-to: out=%q err=%v", out, err)
	}
	if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "v0.1.0" {
		t.Errorf("write-to content = %q, want %q", got, "v0.1.0")
	}
}

// TestTagFormatPushTagTo: --push-tag-to pushes the --tag-format-shaped tag.
func TestTagFormatPushTagTo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0
	url, st := newMemRemote(t)

	if _, err := h.g.r.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin", "--tag-format", "v${full}")
	if err != nil || out != "v0.1.0" {
		t.Fatalf("--tag-format --push-tag-to: out=%q err=%v", out, err)
	}

	// The shaped tag must have been pushed to the remote.
	if got := memTagHash(t, st, "v0.1.0"); got != head {
		t.Errorf("tag v0.1.0 on remote points at %s, want HEAD %s", got, head)
	}
	if got := memTagHash(t, st, "0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("unformatted tag 0.1.0 pushed (%s) but --tag-format should shape the pushed tag", got)
	}
}

// TestTagFormatIgnoredOnNonMain: --tag-format shapes stdout regardless of
// branch, but no tag is made off main.
func TestTagFormatIgnoredOnNonMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "v${full}")
	if err != nil {
		t.Fatalf("--tag-format on develop: out=%q err=%v", out, err)
	}
	if !strings.HasPrefix(out, "v") {
		t.Errorf("stdout %q should still be shaped by --tag-format on a non-main branch", out)
	}
	// No tag should have been created off main.
	iter, err := h.g.r.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	defer iter.Close()
	if ref, err := iter.Next(); err == nil {
		t.Errorf("unexpected tag %s created on non-main branch", ref.Name())
	}
}
