package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version is givi's own version, overridable at build time with
// -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

const usage = `givi - a dumb GitVersion clone

Usage: givi [flag]

Outputs a semantic version derived from the local git tree.
With no flag it prints the version for the current branch and exits.

Flags:
  --help              Show this help and exit.
  --version           Show givi's own version and exit.
  --format <tmpl>     Render the version through a template instead of printing
                      the full version. The template uses ${var} syntax with the
                      variables: full, base, prerelease, increment, major, minor,
                      patch, branch, shortsha, longsha. Doesn't affect tag.
                      Bash parameter expansions are partially supported.
  --tag-format <tmpl> Same as --format, but also affects tag by --tag-main.
  --write-to <file>   Also write the output (honoring --format) to <file>.
  --branch <name>     Branch name. On a detached HEAD it supplies the branch;
                      on a checked-out branch it validates the actual branch name.
  --tag-main          On the main branch, create a lightweight tag with the
                      computed version at HEAD if it does not already exist.
  --push-tag-to <r>   Push the computed tag (nothing else) to remote <r>,
                      given as an existing remote name or a remote URL.
                      Requires --tag-main; ignored on a non-main branch.
  --debug             Trace every calculation step to stderr.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "givi: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	return runWithRepo(nil, args, out, errOut)
}

// runWithRepo is the body of run with the repository injectable. When g is nil
// the repository at the current directory is opened; tests pass an in-memory
// repo to avoid touching disk.
func runWithRepo(g *repo, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("givi", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprint(out, usage) }

	var (
		showHelp      = fs.Bool("help", false, "show help")
		showVersion   = fs.Bool("version", false, "show givi version")
		formatTmpl    = fs.String("format", "", "render the version through a ${var} template")
		tagFormatTmpl = fs.String("tag-format", "", "like --format, but also shapes the tag from --tag-main")
		writeTo       = fs.String("write-to", "", "also write the output to this file (overwriting it)")
		branchName    = fs.String("branch", "", "branch name to compute for; overrides a detached HEAD, must match a checked-out branch")
		tagMain       = fs.Bool("tag-main", false, "tag HEAD on main with the computed version")
		pushTagTo     = fs.String("push-tag-to", "", "push only the computed tag to the given remote name or URL")
		debug         = fs.Bool("debug", false, "trace calculation steps to stderr")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	// --help takes priority over everything.
	if *showHelp {
		fmt.Fprint(out, usage)
		return nil
	}
	// --version takes priority over everything except --help.
	if *showVersion {
		fmt.Fprintln(out, version)
		return nil
	}

	// --push-tag-to only makes sense together with --tag-main; using it alone is
	// a usage error rather than a silent no-op.
	if *pushTagTo != "" && !*tagMain {
		return fmt.Errorf("--push-tag-to requires --tag-main")
	}

	if g == nil {
		var err error
		g, err = openRepo(".")
		if err != nil {
			return err
		}
		defer g.Close()
	}
	branch, err := g.resolveBranch(*branchName)
	if err != nil {
		return err
	}
	head, err := g.headCommit()
	if err != nil {
		return err
	}

	var trace io.Writer
	if *debug {
		trace = errOut
	}
	calc, err := newCalculatorTrace(g, trace)
	if err != nil {
		return err
	}
	res, err := calc.Calculate(branch, head)
	if err != nil {
		return err
	}

	// v is the full computed version, the default for both the tag and the
	// printed output.
	v, err := res.version()
	if err != nil {
		return err
	}

	// tagVal is what --tag-main tags with and --push-tag-to pushes. Only
	// --tag-format reshapes it; a plain --format leaves the tag as the full
	// version.
	tagVal := v
	if *tagFormatTmpl != "" {
		tagVal, err = res.renderFormat(*tagFormatTmpl)
		if err != nil {
			return err
		}
	}

	if *tagMain {
		if res.isMain {
			created, err := g.createLightweightTag(tagVal, head.Hash)
			if err != nil {
				return err
			}
			if created {
				calc.logf("tag-main: created lightweight tag %q at %s", tagVal, short(head.Hash))
			} else {
				calc.logf("tag-main: tag %q already exists; leaving it unchanged", tagVal)
			}

			// --push-tag-to only takes effect alongside --tag-main on main. The
			// tag must mark HEAD, whether givi just created it above or it was
			// already present (lightweight or annotated) on this commit.
			if *pushTagTo != "" {
				if err := g.verifyTagAtHead(tagVal, head.Hash); err != nil {
					return err
				}
				if err := g.pushTag(tagVal, *pushTagTo); err != nil {
					return err
				}
				calc.logf("push-tag-to: pushed tag %q to %q", tagVal, redactURL(*pushTagTo))
			}
		} else {
			calc.logf("tag-main: ignored on non-main branch %q", branch)
			if *pushTagTo != "" {
				calc.logf("push-tag-to: ignored on non-main branch %q", branch)
			}
		}
	}

	// A --format template reshapes the printed output (and --write-to); the
	// default is the full version. When only --tag-format is given it stands in
	// for --format here too, so a lone --tag-format shapes stdout as well as the
	// tag. When both are given, --format wins for the printed output.
	stdoutTmpl := *formatTmpl
	if stdoutTmpl == "" {
		stdoutTmpl = *tagFormatTmpl
	}
	out2 := v
	if stdoutTmpl != "" {
		out2, err = res.renderFormat(stdoutTmpl)
		if err != nil {
			return err
		}
	}

	// --write-to persists the same output (honoring --format) to a file,
	// overwriting it if present. The version is still printed to stdout below.
	if *writeTo != "" {
		if err := g.writeOutput(*writeTo, []byte(out2+"\n"), 0o644); err != nil {
			return fmt.Errorf("write-to %q: %w", *writeTo, err)
		}
		calc.logf("write-to: wrote output to %q", *writeTo)
	}

	fmt.Fprintln(out, out2)
	return nil
}
