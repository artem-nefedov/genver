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
//	SHA         the full HEAD commit hash (40 hex chars)
type formatVars struct {
	Full       string
	Core       string
	PreRelease string
	Count      string
	Major      uint64
	Minor      uint64
	Patch      uint64
	Branch     string
	SHA        string
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
		SHA:        r.headHash.String(),
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
