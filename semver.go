package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// bumpKind identifies how much of the version core to increment.
// Ordering matters: patch < minor < major, so max() picks the strongest bump.
type bumpKind int

const (
	bumpNone  bumpKind = iota // no increment
	bumpPatch                 // a.b.c -> a.b.(c+1)
	bumpMinor                 // a.b.c -> a.(b+1).0
	bumpMajor                 // a.b.c -> (a+1).0.0
)

func maxBump(a, b bumpKind) bumpKind {
	if a > b {
		return a
	}
	return b
}

func (k bumpKind) String() string {
	switch k {
	case bumpMajor:
		return "major"
	case bumpMinor:
		return "minor"
	case bumpPatch:
		return "patch"
	default:
		return "none"
	}
}

// semverCodeRe matches the special "+semver:" directive in a commit message.
var semverCodeRe = regexp.MustCompile(`\+semver:\s*(major|minor|patch)`)

// bumpFromMessage returns the bump requested by a "+semver:" code in the
// message, or bumpNone if there is none.
func bumpFromMessage(msg string) bumpKind {
	m := semverCodeRe.FindStringSubmatch(strings.ToLower(msg))
	if m == nil {
		return bumpNone
	}
	switch m[1] {
	case "major":
		return bumpMajor
	case "minor":
		return bumpMinor
	default:
		return bumpPatch
	}
}

// core is a bare major.minor.patch version with no prerelease or metadata.
type core struct {
	major, minor, patch uint64
}

func coreFromVersion(v *semver.Version) core {
	return core{v.Major(), v.Minor(), v.Patch()}
}

// parseStrictTag parses a tag value into a semver version using the STRICT
// parser, after stripping a single optional leading "v" (the common git tag
// convention). Strict parsing rejects partial versions ("v2", "2.1"), leading
// zeros ("01.2.3", "2020.01.15"), and any other non-conforming form, so those
// tags are ignored during calculation. Full releases ("1.2.3", "v1.2.3"), valid
// CalVer without leading zeros ("2020.12.15"), build metadata ("1.2.3+build"),
// and prereleases ("1.2.3-rc.1") are accepted.
func parseStrictTag(s string) (*semver.Version, error) {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, "v")
	v, err := semver.StrictNewVersion(t)
	if err != nil {
		return nil, fmt.Errorf("invalid semver %q: %w", s, err)
	}
	return v, nil
}

// parseCore parses a release-tag value such as "v2.1.0" or "2.1.0" into a core.
//
// Only a bare release version (optionally with build metadata, e.g. "1.2.3+b")
// is accepted. A prerelease version such as "1.2.3-rc.1" is rejected: a
// prerelease is by definition "not yet released", so it must never establish a
// release boundary. Build metadata is ignored, as it does not change the release
// the tag denotes.
func parseCore(s string) (core, error) {
	v, err := parseStrictTag(s)
	if err != nil {
		return core{}, err
	}
	if v.Prerelease() != "" {
		return core{}, fmt.Errorf("prerelease version %q is not a release", s)
	}
	return coreFromVersion(v), nil
}

func (c core) String() string {
	return fmt.Sprintf("%d.%d.%d", c.major, c.minor, c.patch)
}

// prereleaseRef is a prerelease tag that carries a trailing numeric counter,
// e.g. "4.5.6-foobar-x.3" -> core 4.5.6, label "foobar-x", counter 3. Such a tag
// is not a release boundary but a "reference point": it pins the in-progress
// version's core, label, and counter so calculation continues from it.
type prereleaseRef struct {
	core    core
	label   string // prerelease identifiers minus the trailing counter
	counter int
}

// preTailRe splits a prerelease tail into its label and trailing numeric
// counter, e.g. "foobar-x.3" -> ("foobar-x", "3") and "alpha.42" -> ("alpha",
// "42"). The counter is the final dot-separated identifier when it is a run of
// digits; a prerelease without such a trailing number has no counter.
var preTailRe = regexp.MustCompile(`^(.*)\.(\d+)$`)

// parsePrereleaseRef parses a tag value into a prerelease reference. It succeeds
// only for a semver prerelease that carries a trailing numeric counter (e.g.
// "4.5.6-foobar-x.3"). A bare release ("1.2.3"), a non-semver name, or a
// prerelease without a trailing counter (e.g. "1.2.3-rc") is rejected.
func parsePrereleaseRef(s string) (prereleaseRef, error) {
	v, err := parseStrictTag(s)
	if err != nil {
		return prereleaseRef{}, err
	}
	pre := v.Prerelease()
	if pre == "" {
		return prereleaseRef{}, fmt.Errorf("%q is a release, not a prerelease reference", s)
	}
	m := preTailRe.FindStringSubmatch(pre)
	if m == nil {
		return prereleaseRef{}, fmt.Errorf("prerelease %q has no trailing numeric counter", s)
	}
	label := m[1]
	if label == "" {
		return prereleaseRef{}, fmt.Errorf("prerelease %q has no label before its counter", s)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return prereleaseRef{}, fmt.Errorf("prerelease counter in %q: %w", s, err)
	}
	return prereleaseRef{core: coreFromVersion(v), label: label, counter: n}, nil
}

// bump applies a single increment, resetting lower components.
func (c core) bump(k bumpKind) core {
	switch k {
	case bumpMajor:
		return core{c.major + 1, 0, 0}
	case bumpMinor:
		return core{c.major, c.minor + 1, 0}
	case bumpPatch:
		return core{c.major, c.minor, c.patch + 1}
	default:
		return c
	}
}

// labelSanitizeRe matches every run of characters that are illegal in a
// semver prerelease identifier / docker tag component.
var labelSanitizeRe = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// sanitizeLabel turns a branch name into a prerelease identifier that is valid
// in both semver 2.0.0 and docker image tags. The branch "type" prefix (the
// part before the first "/") is stripped first, e.g.
// "bugfix/ABC-123-foo_bar" -> "ABC-123-foo-bar".
func sanitizeLabel(branch string) string {
	name := branch
	if i := strings.LastIndex(branch, "/"); i >= 0 {
		name = branch[i+1:]
	}
	name = labelSanitizeRe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	return name
}

// dockerTagRe validates the final version string against the docker tag grammar
// (also a subset of what semver allows): [A-Za-z0-9_][A-Za-z0-9_.-]{0,127}.
var dockerTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// format builds the final version string and guards that it is a valid semver
// AND a valid docker tag. prerelease is the full tail (e.g. "alpha.4" or
// "ABC-123.0"); empty means a plain release version.
func format(c core, prerelease string) (string, error) {
	v := c.String()
	if prerelease != "" {
		v += "-" + prerelease
	}
	// Must parse as strict semver.
	if _, err := semver.StrictNewVersion(v); err != nil {
		return "", fmt.Errorf("generated version %q is not valid semver: %w", v, err)
	}
	// Must be a valid docker tag (no build metadata, no illegal chars).
	if !dockerTagRe.MatchString(v) {
		return "", fmt.Errorf("generated version %q is not a valid docker tag", v)
	}
	return v, nil
}
