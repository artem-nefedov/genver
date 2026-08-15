package main

import (
	"fmt"
	"regexp"
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

// parseCore parses a tag value such as "v2.1.0" or "2.1.0" into a core,
// discarding any prerelease/metadata tail.
func parseCore(s string) (core, error) {
	v, err := semver.NewVersion(strings.TrimSpace(s))
	if err != nil {
		return core{}, fmt.Errorf("invalid semver %q: %w", s, err)
	}
	return coreFromVersion(v), nil
}

func (c core) String() string {
	return fmt.Sprintf("%d.%d.%d", c.major, c.minor, c.patch)
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
