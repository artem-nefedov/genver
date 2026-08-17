package main

import (
	"testing"
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
