# GenVer - Git SemVer generator

A small [GitVersion](https://github.com/gittools/gitversion)-inspired tool with zero configuration.

Generates semver-compatible version based on git tree and current HEAD.

## Installation

### Homebrew (macOS & Linux)

```sh
brew install artem-nefedov/tap/genver
```

### Download binary

Prebuilt binaries for Linux/macOS/Windows (amd64/arm64) are available on the
[Releases](https://github.com/artem-nefedov/genver/releases) page.

### Go install

```sh
go install github.com/artem-nefedov/genver@latest
```

### Docker images

Images of two types (distroless and alpine) are available:

```
ghcr.io/artem-nefedov/genver:latest
ghcr.io/artem-nefedov/genver:latest-alpine
```

Change "latest" to specific release to pin the version.

## Differences compared to GitVersion

- Self-contained statically-linked binary, doesn't rely on a runtime or other executables
- Is fast
- Just outputs the version by default
- Can make a guess if "v" prefix needs to be added based on existing tags
- Version is a valid OCI/Docker tag in addition to semver compliance
- Recognizes `+semver` messages and feature branch merge messages (native, GitHub, Bitbucket Server)
- Allows overriding bump level on merge commits with `+semver` directive
- Allows usage of pre-release "reference" tags
- Never fetches, but still takes remotes into account
- Never stores cache on disk, avoiding inconsistent behavior
- Supports a few extra tricks, like formatting output and creating/pushing tags
- Calculation logic may intentionally differ in some cases
- Works only with the specific flow (see below)

## Supported flow

Git repo needs to be structured like this:

- There is a permanent main branch called "main" or "master"
- Optionally, there is a permanent "develop" branch created from main branch, from which merge commits are made to main, which creates a new release
- There are short-lived branches created from "develop", where the work is done, and they are then merged into "develop" and deleted
- Also works if "develop" does not exist and short-lived branches are created from main branch
- Short-lived branches are prefixed with feature/, feat/, bugfix/, hotfix/, etc.
- Releases on main branch are (preferably) tagged

For more details, see ALGORITHM.md
