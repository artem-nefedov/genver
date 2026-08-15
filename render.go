package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fluxcd/pkg/envsubst"
)

// renderFormat expands a --format template into the final output string. The
// template uses ${var} syntax (as understood by fluxcd's envsubst) and may
// reference these variables — and only these; a reference to any other variable
// fails in strict mode:
//
//	full        the complete version, e.g. "1.2.3-alpha.4" or "1.2.3"
//	base        the version core without the prerelease tail, e.g. "1.2.3"
//	prerelease  the prerelease tail including its leading dash, e.g. "-alpha.4",
//	            or an empty string on a release version
//	increment   the trailing counter of the prerelease tail (the number after
//	            the last dot), e.g. "4", or an empty string on a release version
//	major       the core's major component, e.g. "1"
//	minor       the core's minor component, e.g. "2"
//	patch       the core's patch component, e.g. "3"
//	branch      the exact name of the branch, e.g. "feature/cool-abc"
//	shortsha    the abbreviated HEAD commit hash, e.g. "0e6df221"
//	longsha     the full HEAD commit hash, e.g. "0e6df221..." (40 hex chars)
//
// Expansion is strict (envsubst is given a mapping that reports any other name
// as unset), so a typo like "${prerlease}" is an error rather than a silent
// empty string.
func (r result) renderFormat(tmpl string) (string, error) {
	full, err := r.version()
	if err != nil {
		return "", err
	}
	base := r.core.String()
	prerelease := ""
	increment := ""
	if r.prerelease != "" {
		prerelease = "-" + r.prerelease
		// The tail is "<label>.<n>"; the increment is the segment after the last
		// dot. On a release the tail is empty, so the increment is too.
		if i := strings.LastIndex(r.prerelease, "."); i >= 0 {
			increment = r.prerelease[i+1:]
		}
	}
	vars := map[string]string{
		"full":       full,
		"base":       base,
		"prerelease": prerelease,
		"increment":  increment,
		"major":      strconv.FormatUint(r.core.major, 10),
		"minor":      strconv.FormatUint(r.core.minor, 10),
		"patch":      strconv.FormatUint(r.core.patch, 10),
		"branch":     r.branch,
		"shortsha":   short(r.headHash),
		"longsha":    r.headHash.String(),
	}
	// Strict mapping: only the known variables resolve; anything else reports
	// exists=false so envsubst fails instead of substituting an empty string.
	out, err := envsubst.Eval(tmpl, func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	})
	if err != nil {
		return "", fmt.Errorf("expand --format %q: %w", tmpl, err)
	}
	return out, nil
}
