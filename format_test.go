package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestTagFormatOnly: with only --tag-format, it shapes the tag created by
// --tag-main but leaves stdout as the full computed version.
func TestTagFormatOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "v{{.Full}}")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--tag-format only: out=%q err=%v, want stdout %q", out, err, "0.1.0")
	}

	// The tag must use the --tag-format template.
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

// TestTagFormatWriteTo: --tag-format never affects --write-to; the file gets
// the full computed version, not the tag-shaped one.
func TestTagFormatWriteTo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0

	out, err := runCapture(t, h, "--tag-main", "--tag-format", "v{{.Full}}", "--write-to", "version.txt")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--tag-format --write-to: out=%q err=%v", out, err)
	}
	if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "0.1.0" {
		t.Errorf("write-to content = %q, want %q", got, "0.1.0")
	}
}

// TestTagFormatRequiresTagMain asserts --tag-format without --tag-main is a
// usage error (not a silent no-op), and that nothing is tagged as a result.
func TestTagFormatRequiresTagMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	_, _, err := runCaptureAll(t, h, "--tag-format", "v{{.Full}}")
	if err == nil {
		t.Fatal("expected error for --tag-format without --tag-main")
	}
	if !strings.Contains(err.Error(), "requires --tag-main") {
		t.Errorf("unexpected error: %v", err)
	}
	// No tag should have been created.
	if _, err := h.g.r.Reference(plumbing.NewTagReferenceName("0.1.0"), false); err == nil {
		t.Error("tag 0.1.0 was created despite the usage error")
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
	if err != nil || out != "0.1.0" {
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

// TestTagFormatIgnoredOnNonMain: --tag-format never shapes stdout, and no tag
// is made off main.
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
	if strings.HasPrefix(out, "v") {
		t.Errorf("stdout %q should not be shaped by --tag-format", out)
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

// TestFormatFor covers --format-for, which gates --format to branches whose name
// starts with the given prefix. On a non-matching branch the default output is
// printed; --tag-format is never affected.
func TestFormatFor(t *testing.T) {
	t.Parallel()

	// Matching branch: --format applies.
	t.Run("MatchApplies", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("feature/x")
		h.commit("c1")

		out, err := runCapture(t, h, "--format", "out-{{.Full}}", "--format-for", "feature/")
		if err != nil {
			t.Fatalf("matching branch: err=%v", err)
		}
		if !strings.HasPrefix(out, "out-") {
			t.Errorf("on matching branch --format should apply, got %q", out)
		}
	})

	// Non-matching branch: --format is dropped, the default output is printed.
	t.Run("NonMatchIgnored", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")
		h.newBranch("develop")
		h.commit("d1")

		out, err := runCapture(t, h, "--format", "out-{{.Full}}", "--format-for", "feature/")
		if err != nil {
			t.Fatalf("non-matching branch: err=%v", err)
		}
		if out != "0.1.1-alpha.1" {
			t.Errorf("on non-matching branch --format should be ignored, got %q, want %q", out, "0.1.1-alpha.1")
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

		out, err := runCapture(t, h, "--branch", "feature/x", "--format", "out-{{.Full}}", "--format-for", "feature/")
		if err != nil {
			t.Fatalf("branch override: err=%v", err)
		}
		if !strings.HasPrefix(out, "out-") {
			t.Errorf("--format-for should match the --branch override, got %q", out)
		}
	})

	// --format-for does not affect --tag-format: the tag is shaped on main even
	// though "main" does not match the prefix, while stdout stays the default.
	t.Run("DoesNotAffectTagFormat", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		head := h.commit("root") // main = 0.1.0

		out, err := runCapture(t, h,
			"--tag-main",
			"--format", "out-{{.Full}}",
			"--format-for", "feature/",
			"--tag-format", "v{{.Full}}",
		)
		if err != nil {
			t.Fatalf("with --tag-format: err=%v", err)
		}
		// stdout uses the default (--format gated off on "main").
		if out != "0.1.0" {
			t.Errorf("stdout = %q, want default %q", out, "0.1.0")
		}
		// The tag is still shaped by --tag-format.
		if got := localTagHash(t, h, "v0.1.0"); got != head {
			t.Errorf("tag v0.1.0 points at %s, want HEAD %s", got, head)
		}
	})

	// --format-for without --format is a usage error.
	t.Run("RequiresFormat", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root")

		_, _, err := runCaptureAll(t, h, "--format-for", "feature/")
		if err == nil {
			t.Fatal("expected error for --format-for without --format")
		}
		if !strings.Contains(err.Error(), "requires --format") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// --format-for also gates --write-to (which follows --format): a non-matching
	// branch writes the default output.
	t.Run("GatesWriteTo", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.commit("root") // main = 0.1.0

		out, err := runCapture(t, h,
			"--format", "out-{{.Full}}",
			"--format-for", "feature/",
			"--write-to", "version.txt",
		)
		if err != nil {
			t.Fatalf("write-to gated: err=%v", err)
		}
		if out != "0.1.0" {
			t.Errorf("stdout = %q, want default %q", out, "0.1.0")
		}
		if got := strings.TrimSpace(h.readWriteTo("version.txt")); got != "0.1.0" {
			t.Errorf("write-to content = %q, want default %q", got, "0.1.0")
		}
	})
}
