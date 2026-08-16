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

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "v{{.Full}}")
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

	out, err := runCapture(t, h, "--tag-main", "--format", "v{{.Full}}")
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
		"--format", "out-{{.Full}}",
		"--tag-format", "tag-{{.Full}}",
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

	out, err := runCapture(t, h, "--tag-format", "v{{.Full}}", "--write-to", "version.txt")
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

	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin", "--tag-format", "v{{.Full}}")
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

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "v{{.Full}}")
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

// writeToFileCount returns how many regular files --write-to persisted directly
// in dir (subdirectories are counted as one entry, not recursed).
func writeToFileCount(t *testing.T, h *harness, dir string) int {
	t.Helper()
	entries, err := h.wfs.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// TestWriteToTemplate: --write-to is itself rendered through the --format
// template, so the target path can embed version variables.
func TestWriteToTemplate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--write-to", "versions/{{.Core}}.txt")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--write-to template: out=%q err=%v", out, err)
	}
	if got := strings.TrimSpace(h.readWriteTo("versions/0.1.0.txt")); got != "0.1.0" {
		t.Errorf("write-to content = %q, want %q", got, "0.1.0")
	}
}

// TestWriteToMultipleFiles: every non-blank line of the rendered --write-to
// argument is a separate file, with leading/trailing whitespace trimmed and
// blank lines ignored. Interior whitespace in a path is preserved.
func TestWriteToMultipleFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	// A multi-line argument: blank and whitespace-only lines are skipped;
	// surrounding whitespace is trimmed; the interior space in "dir/a b.txt"
	// is kept.
	tmpl := "  first.txt  \n\n   \n\tsecond.txt\ndir/a b.txt\n"
	out, err := runCapture(t, h, "--format", "v{{.Full}}", "--write-to", tmpl)
	if err != nil || out != "v0.1.0" {
		t.Fatalf("--write-to multi: out=%q err=%v", out, err)
	}

	for _, name := range []string{"first.txt", "second.txt", "dir/a b.txt"} {
		if got := strings.TrimSpace(h.readWriteTo(name)); got != "v0.1.0" {
			t.Errorf("write-to %q content = %q, want %q", name, got, "v0.1.0")
		}
	}
	// Exactly three files were written: the whitespace-only lines were skipped.
	if got := writeToFileCount(t, h, "/"); got != 2 { // first.txt, second.txt in root
		t.Errorf("root file count = %d, want 2 (dir/ holds the third)", got)
	}
}

// TestWriteToBlankRendersNoFiles: a template that renders to only whitespace
// writes nothing, and is not an error.
func TestWriteToBlankRendersNoFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	out, err := runCapture(t, h, "--write-to", "   \n\t\n")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--write-to blank: out=%q err=%v", out, err)
	}
	if got := writeToFileCount(t, h, "/"); got != 0 {
		t.Errorf("expected no files written, got %d", got)
	}
}

// TestWriteToDirectoryPathFails: a line ending in "/" names a directory, which
// is not a valid write target, so the run fails early and writes nothing.
func TestWriteToDirectoryPathFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	if _, err := runCapture(t, h, "--write-to", "versions/"); err == nil {
		t.Error("--write-to path ending in / should error")
	}
	// A trailing slash after trimming surrounding whitespace must also fail.
	if _, err := runCapture(t, h, "--write-to", "  out/  "); err == nil {
		t.Error("--write-to trimmed path ending in / should error")
	}
	// Nothing should have been written.
	if got := writeToFileCount(t, h, "/"); got != 0 {
		t.Errorf("expected no files written, got %d", got)
	}
}

// TestWriteToDirectoryPathFailsEarly: when a directory line follows a valid
// file line, the run still fails; the earlier file may already be written, but
// the directory line never becomes a target.
func TestWriteToDirectoryPathFailsEarly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	if _, err := runCapture(t, h, "--write-to", "ok.txt\nbad/"); err == nil {
		t.Error("--write-to with a directory line should error")
	}
	// The "bad/" directory line must not have produced a directory entry.
	if writeToFileCount(t, h, "/") > 1 {
		t.Errorf("unexpected extra entries written for the directory line")
	}
}
