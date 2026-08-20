package main

import (
	"fmt"
	"strings"
	"text/template"

	sprig "github.com/Masterminds/sprig/v3"
)

// formatVars is the data passed to a --format / --tag-format Go template. Every
// exported field is a template variable; referencing anything else is a
// parse-time error, so a typo like "{{.Prerlease}}" fails rather than expanding
// to an empty string.
//
//	Full        the complete version, e.g. "1.2.3-alpha.4" or "1.2.3"
//	Core        the version core without the prerelease tail, e.g. "1.2.3"
//	PreRelease  the prerelease tail including its leading dash, e.g. "-alpha.4",
//	            or an empty string on a release version
//	Count       the trailing counter of the prerelease tail (the number after
//	            the last dot), e.g. "4", or an empty string on a release version
//	Major       the core's major component as an integer, e.g. 1
//	Minor       the core's minor component as an integer, e.g. 2
//	Patch       the core's patch component as an integer, e.g. 3
//	Branch      the exact name of the branch, e.g. "feature/cool-abc"
//	HeadHash    the full HEAD commit hash (40 hex chars)
type formatVars struct {
	Full       string
	Core       string
	PreRelease string
	Count      string
	Major      uint64
	Minor      uint64
	Patch      uint64
	Branch     string
	HeadHash   string
}

// renderFormat expands a --format template into the final output string. The
// template is a Go text/template referencing the fields of formatVars, e.g.
// "{{.Core}}{{.PreRelease}}" or "v{{.Full}}". Integer components (Major, Minor,
// Patch) are passed as integers so template arithmetic and formatting work on
// them; the rest are strings. The Sprig function library is available.
//
// Referencing an unknown field is a parse-time error, so a typo like
// "{{.Prerlease}}" fails instead of silently rendering nothing.
func (r result) renderFormat(tmpl string, allowNonHermetic bool) (string, error) {
	full, err := r.version()
	if err != nil {
		return "", err
	}
	prerelease := ""
	count := ""
	if r.prerelease != "" {
		prerelease = "-" + r.prerelease
		// The tail is "<label>.<n>"; the count is the segment after the last
		// dot. On a release the tail is empty, so the count is too.
		if i := strings.LastIndex(r.prerelease, "."); i >= 0 {
			count = r.prerelease[i+1:]
		}
	}
	data := formatVars{
		Full:       full,
		Core:       r.core.String(),
		PreRelease: prerelease,
		Count:      count,
		Major:      r.core.major,
		Minor:      r.core.minor,
		Patch:      r.core.patch,
		Branch:     r.branch,
		HeadHash:   r.headHash.String(),
	}

	var funcs template.FuncMap
	if allowNonHermetic {
		funcs = sprig.TxtFuncMap()
	} else {
		funcs = sprig.HermeticTxtFuncMap()
	}

	t, err := template.New("format").Funcs(funcs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse --format %q: %w", tmpl, err)
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("expand --format %q: %w", tmpl, err)
	}
	return buf.String(), nil
}

// formatForRule is one branch-prefix -> template mapping from a single
// --format-for flag occurrence.
type formatForRule struct {
	prefix   string
	template string
}

// formatForList collects the --format-for rules from a repeatable flag. Each
// occurrence of --format-for contributes one "prefix=template" rule, appended
// in the order given on the command line so the FIRST rule whose prefix matches
// the branch wins (letting a more specific prefix be listed ahead of a broader
// one). It implements flag.Value.
type formatForList []formatForRule

// String renders the collected rules for flag's usage/error machinery.
func (l *formatForList) String() string {
	if l == nil || len(*l) == 0 {
		return ""
	}
	parts := make([]string, len(*l))
	for i, r := range *l {
		parts[i] = r.prefix + "=" + r.template
	}
	return strings.Join(parts, ",")
}

// Set parses one "prefix=template" argument and appends it as a rule. The prefix
// is everything before the FIRST "=", and the template is the remainder (so a
// template may itself contain "=" characters). A missing "=", an empty prefix,
// or an empty template is an error.
func (l *formatForList) Set(arg string) error {
	prefix, tmpl, ok := strings.Cut(arg, "=")
	if !ok {
		return fmt.Errorf("--format-for %q has no '=' separating prefix and template", arg)
	}
	if prefix == "" {
		return fmt.Errorf("--format-for %q has an empty branch prefix", arg)
	}
	if tmpl == "" {
		return fmt.Errorf("--format-for %q has an empty template", arg)
	}
	*l = append(*l, formatForRule{prefix: prefix, template: tmpl})
	return nil
}

// matchFormatFor returns the template of the first --format-for rule whose
// prefix the branch starts with, and whether any rule matched.
func matchFormatFor(rules formatForList, branch string) (string, bool) {
	for _, r := range rules {
		if strings.HasPrefix(branch, r.prefix) {
			return r.template, true
		}
	}
	return "", false
}
