package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestFormatTagOnly: with only --format-tag, it shapes the tag created by
// --tag-main but leaves stdout as the full computed version.
func TestFormatTagOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main", "--format-tag", "v{{.Full}}")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--format-tag only: out=%q err=%v, want stdout %q", out, err, "0.1.0")
	}

	// The tag must use the --format-tag template.
	if got := localTagHash(t, h, "v0.1.0"); got != head {
		t.Errorf("tag v0.1.0 points at %s, want HEAD %s", got, head)
	}
	// The unformatted full version must NOT have been tagged.
	if got := localTagHash(t, h, "0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("unformatted tag 0.1.0 exists (%s) but --format-tag should shape the tag", got)
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

// TestFormatAndFormatTag: when both are given, --format shapes stdout (and
// --write-to) while --format-tag shapes only the tag.
func TestFormatAndFormatTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h,
		"--tag-main",
		"--format", "out-{{.Full}}",
		"--format-tag", "tag-{{.Full}}",
		"--write-to", "version.txt",
	)
	if err != nil || out != "out-0.1.0" {
		t.Fatalf("both formats: out=%q err=%v, want stdout %q", out, err, "out-0.1.0")
	}

	// --write-to follows --format, not --format-tag.
	if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "out-0.1.0" {
		t.Errorf("write-to content = %q, want %q", got, "out-0.1.0")
	}

	// The tag follows --format-tag.
	if got := localTagHash(t, h, "tag-0.1.0"); got != head {
		t.Errorf("tag tag-0.1.0 points at %s, want HEAD %s", got, head)
	}
	if got := localTagHash(t, h, "out-0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("stdout-format tag out-0.1.0 exists (%s) but the tag must use --format-tag", got)
	}
}

// TestFormatTagWriteTo: --format-tag never affects --write-to; the file gets
// the full computed version, not the tag-shaped one.
func TestFormatTagWriteTo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main", "--format-tag", "v{{.Full}}", "--write-to", "version.txt")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--format-tag --write-to: out=%q err=%v", out, err)
	}
	if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "0.1.0" {
		t.Errorf("write-to content = %q, want %q", got, "0.1.0")
	}
}

// TestFormatTagRequiresTagMain asserts --format-tag without --tag-main is a
// usage error (not a silent no-op), and that nothing is tagged as a result.
func TestFormatTagRequiresTagMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	_, _, err := runCaptureAll(t, h, "--format-tag", "v{{.Full}}")
	if err == nil {
		t.Fatal("expected error for --format-tag without --tag-main")
	}
	if !strings.Contains(err.Error(), "requires --tag-main") {
		t.Errorf("unexpected error: %v", err)
	}
	// No tag should have been created.
	if _, err := h.g.r.Reference(plumbing.NewTagReferenceName("0.1.0"), false); err == nil {
		t.Error("tag 0.1.0 was created despite the usage error")
	}
}

// TestFormatTagPushTagTo: --push-tag-to pushes the --format-tag-shaped tag.
func TestFormatTagPushTagTo(t *testing.T) {
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

	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin", "--format-tag", "v{{.Full}}")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--format-tag --push-tag-to: out=%q err=%v", out, err)
	}

	// The shaped tag must have been pushed to the remote.
	if got := memTagHash(t, st, "v0.1.0"); got != head {
		t.Errorf("tag v0.1.0 on remote points at %s, want HEAD %s", got, head)
	}
	if got := memTagHash(t, st, "0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("unformatted tag 0.1.0 pushed (%s) but --format-tag should shape the pushed tag", got)
	}
}

// TestFormatTagIgnoredOnNonMain: --format-tag never shapes stdout, and no tag
// is made off main.
func TestFormatTagIgnoredOnNonMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")

	out, err := runCapture(t, h, "--tag-main", "--format-tag", "v{{.Full}}")
	if err != nil {
		t.Fatalf("--format-tag on develop: out=%q err=%v", out, err)
	}
	if strings.HasPrefix(out, "v") {
		t.Errorf("stdout %q should not be shaped by --format-tag", out)
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

// TestWriteToTemplateExpandsToMultipleFiles: the --write-to argument is a
// single line on the command line, but its template renders to multiple lines,
// each of which names a file. This verifies the multi-file split happens on the
// rendered result (not the raw argument), so a template that emits newlines
// fans out to several files. Empty and whitespace-only rendered lines are
// ignored, exactly as with a literal multi-line argument.
func TestWriteToTemplateExpandsToMultipleFiles(t *testing.T) {
	t.Parallel()

	// A range emitting a newline after each path: the raw argument has no
	// newlines, yet it renders to several lines. Interleaved among the real
	// paths are a fully empty line and a whitespace-only line, both of which
	// must be skipped so only the three real files are written.
	t.Run("RangeEmitsNewlines", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		// Renders to (with \n shown): "a-0.1.0.txt\n\nb-0.1.0.txt\n   \nc-0.1.0.txt\n"
		// i.e. a blank line after "a" and a whitespace-only line after "b".
		arg := `{{range list "a" "" "b" "   " "c"}}{{if eq . ""}}{{else if eq . "   "}}   {{else}}{{.}}-{{$.Core}}.txt{{end}}{{"\n"}}{{end}}`
		out, err := runCapture(t, h, "--format", "v{{.Full}}", "--write-to", arg)
		if err != nil || out != "v0.1.0" {
			t.Fatalf("--write-to range: out=%q err=%v", out, err)
		}

		for _, name := range []string{"a-0.1.0.txt", "b-0.1.0.txt", "c-0.1.0.txt"} {
			if got := strings.TrimSpace(h.readWriteTo(name)); got != "v0.1.0" {
				t.Errorf("write-to %q content = %q, want %q", name, got, "v0.1.0")
			}
		}
		// Exactly three files: the empty and whitespace-only lines were skipped.
		if got := writeToFileCount(t, h, "/"); got != 3 {
			t.Errorf("root file count = %d, want 3", got)
		}
		// Directly assert the whitespace-only line produced no entry under its
		// raw (untrimmed) name.
		if writeToExists(t, h, "   ") {
			t.Errorf(`whitespace-only line created an entry at "   ", want none`)
		}
	})

	// A list joined with a newline: the single-line argument becomes several
	// lines including an empty entry and a whitespace-only entry, both of which
	// are ignored, leaving two files.
	t.Run("JoinWithNewline", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		// Renders to: "x-0.1.0.txt\n\ny-0.1.0.txt\n \t "
		arg := `{{ list (printf "x-%s.txt" .Core) "" (printf "y-%s.txt" .Core) " \t " | join "\n" }}`
		out, err := runCapture(t, h, "--write-to", arg)
		if err != nil || out != "0.1.0" {
			t.Fatalf("--write-to join: out=%q err=%v", out, err)
		}

		for _, name := range []string{"x-0.1.0.txt", "y-0.1.0.txt"} {
			if got := strings.TrimSpace(h.readWriteTo(name)); got != "0.1.0" {
				t.Errorf("write-to %q content = %q, want %q", name, got, "0.1.0")
			}
		}
		// Only two files: the empty and whitespace-only entries were skipped.
		if got := writeToFileCount(t, h, "/"); got != 2 {
			t.Errorf("root file count = %d, want 2", got)
		}
		// Directly assert the whitespace-only entry produced no file under its
		// raw (untrimmed) name.
		if writeToExists(t, h, " \t ") {
			t.Errorf(`whitespace-only entry created an entry at " \t ", want none`)
		}
	})
}

// TestWriteToDuplicatePaths: when the rendered --write-to argument names the
// same file on two lines, the file is written twice (an idempotent overwrite),
// producing a single file with the expected content and no error.
func TestWriteToDuplicatePaths(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--write-to", "dup.txt\ndup.txt")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--write-to duplicate: out=%q err=%v", out, err)
	}
	if got := strings.TrimSpace(h.readWriteTo("dup.txt")); got != "0.1.0" {
		t.Errorf("dup.txt content = %q, want %q", got, "0.1.0")
	}
	// Only one file exists despite the path appearing twice.
	if got := writeToFileCount(t, h, "/"); got != 1 {
		t.Errorf("root file count = %d, want 1", got)
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

// TestFormatRendersEmpty: a --format template that renders to the empty string
// prints just a bare newline to stdout (no error), and when combined with
// --write-to the target file receives that same empty render (a lone newline).
func TestFormatRendersEmpty(t *testing.T) {
	t.Parallel()

	// Bare empty render: trimmed stdout is empty; raw stdout is a lone newline.
	t.Run("StdoutOnly", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		// Trimmed view: empty.
		if out, err := runCapture(t, h, "--format", "{{if false}}x{{end}}"); err != nil || out != "" {
			t.Fatalf("empty --format: out=%q err=%v, want empty", out, err)
		}
		// Raw view: a single trailing newline from Fprintln, nothing else.
		var stdout, stderr bytes.Buffer
		if err := runWithRepo(h.g, []string{"--format", "{{if false}}x{{end}}"}, &stdout, &stderr); err != nil {
			t.Fatalf("empty --format raw: err=%v", err)
		}
		if got := stdout.String(); got != "\n" {
			t.Errorf("raw stdout = %q, want %q", got, "\n")
		}
	})

	// Empty render with --write-to: the file receives the empty render plus the
	// trailing newline the writer appends.
	t.Run("WithWriteTo", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		out, err := runCapture(t, h, "--format", "{{if false}}x{{end}}", "--write-to", "versions.txt")
		if err != nil || out != "" {
			t.Fatalf("empty --format --write-to: out=%q err=%v, want empty", out, err)
		}
		if got := h.readWriteTo("versions.txt"); got != "\n" {
			t.Errorf("write-to file content = %q, want %q", got, "\n")
		}
	})
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

// TestFormatFor covers --format-for, a repeatable "prefix=template" flag that
// shapes the output per branch prefix. A matching branch (first match, in the
// order given, wins) uses its rule's template; a non-matching branch falls back
// to --format, or the default when --format is unset. --format-tag is never
// affected.
func TestFormatFor(t *testing.T) {
	t.Parallel()

	// A matching prefix uses its own template.
	t.Run("MatchUsesRuleTemplate", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("c1")

		out, err := runCapture(t, h, "--format-for", "feature/=feat-{{.Full}}")
		if err != nil {
			t.Fatalf("match: err=%v", err)
		}
		if !strings.HasPrefix(out, "feat-") {
			t.Errorf("matching branch should use its rule template, got %q", out)
		}
	})

	// Works with no --format at all: a non-matching branch prints the default.
	t.Run("NonMatchDefaultWithoutFormat", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")

		out, err := runCapture(t, h, "--format-for", "feature/=feat-{{.Full}}")
		if err != nil {
			t.Fatalf("non-match without --format: err=%v", err)
		}
		if out != "0.1.1-alpha.1" {
			t.Errorf("non-matching branch should print default, got %q want %q", out, "0.1.1-alpha.1")
		}
	})

	// With --format present, a non-matching branch falls back to --format.
	t.Run("NonMatchFallsBackToFormat", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")

		out, err := runCapture(t, h, "--format", "fmt-{{.Full}}", "--format-for", "feature/=feat-{{.Full}}")
		if err != nil {
			t.Fatalf("non-match with --format: err=%v", err)
		}
		if out != "fmt-0.1.1-alpha.1" {
			t.Errorf("non-matching branch should use --format, got %q", out)
		}
	})

	// A matching branch's rule template overrides --format.
	t.Run("MatchOverridesFormat", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("c1")

		out, err := runCapture(t, h, "--format", "fmt-{{.Full}}", "--format-for", "feature/=feat-{{.Full}}")
		if err != nil {
			t.Fatalf("match overrides --format: err=%v", err)
		}
		if !strings.HasPrefix(out, "feat-") {
			t.Errorf("matching rule should override --format, got %q", out)
		}
	})

	// Multiple rules (repeated flags): the first matching prefix wins, so a more
	// specific prefix given first takes precedence over a broader one.
	t.Run("FirstMatchWins", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/special")
		h.commit("c1")

		out, err := runCapture(t, h,
			"--format-for", "feature/special=SPECIAL-{{.Core}}",
			"--format-for", "feature/=feat-{{.Core}}")
		if err != nil {
			t.Fatalf("first-match: err=%v", err)
		}
		if !strings.HasPrefix(out, "SPECIAL-") {
			t.Errorf("first matching rule should win, got %q", out)
		}
	})

	// A later repeated flag still matches when the earlier ones do not.
	t.Run("LaterRuleMatches", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("bugfix/y")
		h.commit("c1")

		out, err := runCapture(t, h,
			"--format-for", "feature/=feat-{{.Core}}",
			"--format-for", "bugfix/=bug-{{.Core}}")
		if err != nil {
			t.Fatalf("later-rule: err=%v", err)
		}
		if !strings.HasPrefix(out, "bug-") {
			t.Errorf("later matching rule should apply, got %q", out)
		}
	})

	// --format-for does not affect --format-tag: the tag is shaped on main even
	// though "main" matches no rule, while stdout uses the default.
	t.Run("DoesNotAffectFormatTag", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		head := h.commit("root") // main = 0.1.0

		out, err := runCapture(t, h,
			"--tag-main",
			"--format-for", "feature/=feat-{{.Full}}",
			"--format-tag", "v{{.Full}}",
		)
		if err != nil {
			t.Fatalf("with --format-tag: err=%v", err)
		}
		if out != "0.1.0" {
			t.Errorf("stdout = %q, want default %q", out, "0.1.0")
		}
		if got := localTagHash(t, h, "v0.1.0"); got != head {
			t.Errorf("tag v0.1.0 points at %s, want HEAD %s", got, head)
		}
	})

	// The prefix is matched against the --branch override on a detached HEAD.
	t.Run("MatchesBranchOverride", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("c1")
		h.detachHead()

		out, err := runCapture(t, h, "--branch", "feature/x", "--format-for", "feature/=feat-{{.Full}}")
		if err != nil {
			t.Fatalf("branch override: err=%v", err)
		}
		if !strings.HasPrefix(out, "feat-") {
			t.Errorf("--format-for should match the --branch override, got %q", out)
		}
	})

	// --write-to follows the chosen (rule) template.
	t.Run("WriteToFollowsRule", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("c1")

		out, err := runCapture(t, h,
			"--format-for", "feature/=feat-{{.Core}}",
			"--write-to", "version.txt",
		)
		if err != nil {
			t.Fatalf("write-to rule: err=%v", err)
		}
		if !strings.HasPrefix(out, "feat-") {
			t.Errorf("stdout = %q, want feat- prefix", out)
		}
		if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != out {
			t.Errorf("write-to content = %q, want %q", got, out)
		}
	})

	// A template containing "=" is preserved (only the first "=" splits).
	t.Run("TemplateMayContainEquals", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("c1")

		out, err := runCapture(t, h, "--format-for", `feature/=a=b-{{.Core}}`)
		if err != nil {
			t.Fatalf("template with '=': err=%v", err)
		}
		if !strings.HasPrefix(out, "a=b-") {
			t.Errorf("template with '=' not preserved, got %q", out)
		}
	})

	// A malformed entry (no "=") is a usage error.
	t.Run("MalformedEntryErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")

		_, _, err := runCaptureAll(t, h, "--format-for", "feature/")
		if err == nil {
			t.Fatal("expected error for entry without '='")
		}
		if !strings.Contains(err.Error(), "no '='") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// An empty prefix is a usage error.
	t.Run("EmptyPrefixErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")

		_, _, err := runCaptureAll(t, h, "--format-for", "=tmpl")
		if err == nil {
			t.Fatal("expected error for empty prefix")
		}
		if !strings.Contains(err.Error(), "empty branch prefix") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// An empty template is a usage error.
	t.Run("EmptyTemplateErrors", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")

		_, _, err := runCaptureAll(t, h, "--format-for", "feature/=")
		if err == nil {
			t.Fatal("expected error for empty template")
		}
		if !strings.Contains(err.Error(), "empty template") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestFormatForList unit-tests the repeatable --format-for flag's parsing
// (formatForList.Set) and String rendering directly.
func TestFormatForList(t *testing.T) {
	t.Parallel()

	t.Run("Multiple", func(t *testing.T) {
		t.Parallel()
		var l formatForList
		if err := l.Set("feature/=a"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := l.Set("bugfix/=b"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		want := formatForList{{"feature/", "a"}, {"bugfix/", "b"}}
		if len(l) != len(want) {
			t.Fatalf("got %d rules, want %d", len(l), len(want))
		}
		for i := range want {
			if l[i] != want[i] {
				t.Errorf("rule %d = %+v, want %+v", i, l[i], want[i])
			}
		}
	})

	t.Run("TemplateWithEquals", func(t *testing.T) {
		t.Parallel()
		var l formatForList
		if err := l.Set("feature/=x=y=z"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if len(l) != 1 || l[0].prefix != "feature/" || l[0].template != "x=y=z" {
			t.Errorf("got %+v", l)
		}
	})

	t.Run("NoEquals", func(t *testing.T) {
		t.Parallel()
		var l formatForList
		if err := l.Set("feature/"); err == nil {
			t.Error("expected error for arg without '='")
		}
	})

	t.Run("EmptyPrefix", func(t *testing.T) {
		t.Parallel()
		var l formatForList
		if err := l.Set("=tmpl"); err == nil {
			t.Error("expected error for empty prefix")
		}
	})

	t.Run("EmptyTemplate", func(t *testing.T) {
		t.Parallel()
		var l formatForList
		if err := l.Set("feature/="); err == nil {
			t.Error("expected error for empty template")
		}
	})

	t.Run("String", func(t *testing.T) {
		t.Parallel()
		var l formatForList
		if got := l.String(); got != "" {
			t.Errorf("empty String = %q, want \"\"", got)
		}
		_ = l.Set("feature/=a")
		_ = l.Set("bugfix/=b")
		if got := l.String(); got != "feature/=a,bugfix/=b" {
			t.Errorf("String = %q", got)
		}
	})
}
