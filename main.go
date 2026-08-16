package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is givi's own version, overridable at build time with
// -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

const usage = `givi - a dumb GitVersion clone

Usage: givi [flag]

Outputs a semantic version derived from the local git tree.
With no flag it prints the version for the current branch and exits.
Default is format is '{{.Full}}', or 'v{{.Full}}' if v-prefixed tag is found.

Flags:
  --help              Show this help and exit.
  --version           Show givi's own version and exit.
  --format <tmpl>     Render the version through a template instead of printing
                      the default. <tmpl> is a Go template with exposed fields:
                      .Full, .Core, .Major, .Minor, .Patch, .PreRelease, .Count,
                      .HeadHash, .Branch. Allows usage of Sprig functions
                      (only hermetic by default). Doesn't affect tag.
  --tag-format <tmpl> Like --format, but only affects the tag by --tag-main.
  --write-to <tmpl>   Also write the output (honoring --format) to one or more
                      files. The argument is a Go template like --format. Every
                      non-blank line produced is a file path (whitespace trimmed).
  --allow-nonhermetic Expose all Sprig template functions in --format/etc.,
                      including non-repeatable ones such as env, now and uuidv4.
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
		formatTmpl    = fs.String("format", "", "render the version through a Go template")
		tagFormatTmpl = fs.String("tag-format", "", "like --format, but only shapes the tag from --tag-main")
		writeTo       = fs.String("write-to", "", "also write the output to the file(s) named by this template, one per non-blank line")
		allowNonHerm  = fs.Bool("allow-nonhermetic", false, "expose all Sprig template functions, including non-repeatable ones (env, now, uuidv4, ...)")
		branchName    = fs.String("branch", "", "branch name to compute for; overrides a detached HEAD, must match a checked-out branch")
		tagMain       = fs.Bool("tag-main", false, "tag HEAD on main with the computed version")
		pushTagTo     = fs.String("push-tag-to", "", "push only the computed tag to the given remote name or URL")
		debug         = fs.Bool("debug", false, "trace calculation steps to stderr")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() > 0 {
		fmt.Fprint(out, usage + "\n")
		return fmt.Errorf("unexpected positional argument: %q", fs.Arg(0))
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

	if *tagFormatTmpl != "" && !*tagMain {
		return fmt.Errorf("--tag-format requires --tag-main")
	}

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

	// v is the full computed version. defaultVal is the value used for the tag
	// and the printed output when no explicit --tag-format / --format is given:
	// it inherits the boundary tag's own spelling, so a repository tagged with
	// a leading "v" (e.g. "v1.2.3") keeps the "v" by default, while an untagged
	// or bare-tagged repository stays bare. An explicit template always wins.
	v, err := res.version()
	if err != nil {
		return err
	}
	var defaultVal string
	if res.vPrefix {
		defaultVal = "v" + v
	} else {
		defaultVal = v
	}

	// tagVal is what --tag-main tags with and --push-tag-to pushes. --tag-format
	// reshapes it explicitly; otherwise it defaults to the boundary-inherited
	// spelling.
	var tagVal string
	if *tagFormatTmpl == "" {
		tagVal = defaultVal
	} else {
		tagVal, err = res.renderFormat(*tagFormatTmpl, *allowNonHerm)
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

	var outText string
	if *formatTmpl == "" {
		outText = defaultVal
	} else {
		outText, err = res.renderFormat(*formatTmpl, *allowNonHerm)
		if err != nil {
			return err
		}
	}

	// --write-to persists the same output (honoring --format) to one or more
	// files, overwriting each if present. Its argument is rendered through the
	// same template as --format; every non-blank line of the result names a
	// file (leading/trailing whitespace trimmed) that the output is written to.
	// The version is still printed to stdout below.
	if *writeTo != "" {
		rendered, err := res.renderFormat(*writeTo, *allowNonHerm)
		if err != nil {
			return fmt.Errorf("write-to %q: %w", *writeTo, err)
		}
		for line := range strings.SplitSeq(rendered, "\n") {
			name := strings.TrimSpace(line)
			if name == "" {
				continue
			}
			if strings.HasSuffix(name, "/") {
				return fmt.Errorf(`write-to %q: path ends in "/", expected a file`, name)
			}
			if err := g.writeOutput(name, []byte(outText+"\n"), 0o644); err != nil {
				return fmt.Errorf("write-to %q: %w", name, err)
			}
			calc.logf("write-to: wrote output to %q", name)
		}
	}

	fmt.Fprintln(out, outText)
	return nil
}
