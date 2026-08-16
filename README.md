# GiVI - Git Version Increment

A dumb [GitVersion](https://github.com/gittools/gitversion) clone with zero configuration.

Generates semver-compatible version based on git tree and current HEAD.

## Installation

### Homebrew (macOS & Linux)

```sh
brew install artem-nefedov/tap/givi
```

### Download binary

Pre-built binaries for Linux and macOS (amd64/arm64) are available on the
[Releases](https://github.com/artem-nefedov/givi/releases) page.

### Docker images

Images of two types (distroless and alpine) are available:

```
ghcr.io/artem-nefedov/givi:latest
ghcr.io/artem-nefedov/givi:latest-alpine
```

Change "latest" to specific release to pin the version.

## Features

- Self-contained statically-linked binary, doesn't rely on other executables
- Fast
- Supports a few extra tricks, like formatting output and creating/pushing tags
- Works only with the specific flow (see below)

## Supported flow

Git repo needs to be generated is structured like this:

- There is a permanent main branch called "main" or "master"
- Optionally, there is a permanent "develop" branch created from main branch, from which merge commits are made to main, which creates a new release
- There are short-lived branches created from "develop", where the work is done, and they are then merged into "develop" and deleted
- Also works if "develop" does not exist and short-lived branches are created from main branch
- Releases (merge commits) on main branch are tagged

## Calculating version

- If no tags: account for all commits throught the common ancestry from the main root (root commit on main is treated as "0.1.0")
- If tag exist: take the nearest found tag as a base and stop further descent down the tree
- Only tags that are a bare release version are used as release boundaries. Tags are parsed with the STRICT semver parser after stripping a single optional leading "v", so "1.2.3", "v1.2.3", "1.2.3+build", and valid CalVer without leading zeros like "2020.12.15" are accepted, while non-strict forms — partial versions ("v2", "2.1") and leading-zero segments ("01.2.3", "2020.01.15") — are ignored, as is any non-semver tag or a prerelease tag without a trailing counter (e.g. "1.2.3-rc")
- A prerelease tag that carries a trailing numeric counter (e.g. "4.5.6-foobar-x.3") is a "reference point", not a boundary: on a non-main branch it pins the in-progress version. On the tagged commit the version is the tag verbatim ("4.5.6-foobar-x.3"); the next commit continues the counter with the tag's label ("4.5.6-foobar-x.4"). When merged to develop it becomes "4.5.6-alpha.\<count\>", and when released to main it becomes "4.5.6". The reference core only takes over when it is higher than the normally-computed core; otherwise it is ignored.
- If a single commit carries tags that resolve to different versions (e.g. "1.0.0" and "4.0.0", or a release and a prerelease reference), it is ambiguous. This is an error only when that commit is actually relevant to the computed version; a conflict on an older commit superseded by a later clean tag is ignored. Tags that resolve to the same version (e.g. "1.2.3" and "v1.2.3", or "1.2.3" and "1.2.3+build") are not a conflict.
- Versions on main branch must always be release ones (e.g. "0.1.0", "3.4.5", etc.)
- Versions on "develop" branch must always have a.b.c-alpha.xy format, e.g. "2.3.0-alpha.43"
- Versions on other branches are based on branch name, ignoring branch type prefix (part before "/"); e.g. on branch "bugfix/debug_bootstrap" the version may be "0.57.0-debug-bootstrap.42"
- Whenever major, minor, or patch version is incremented on non-main branch, the counter at the end resets
- On develop, counter resets to 1. Direct commits increment it by 1. Merge commits - by the number of commits inside the merge.
- On other branches: while it has no extra commits compared to develop, the counter is 0. Direct commits increment it by 1. Merge commits - by the number of commits inside the merge.
- A branch is versioned relative to its fork point on develop's mainline, so once it is merged back into develop its own commits still count (its version stays stable) until the branch advances or is deleted
- If develop is merged INTO a feature/bugfix/etc. branch, the branch's fork point advances up develop's mainline to the merged develop tip, so it inherits develop's accumulated bump: a patch-only develop keeps the branch's core unchanged (only the trailing counter advances), while a develop that had reason to bump minor or major raises the branch's increment to minor or major accordingly (feature branches keep their minor floor)
- Direct commits on main increment patch, unless commit message contains special code "+semver: minor" (bumps minor) or "+semver: major" (bumps major)
- Direct commits on develop increment patch, unless commit message contains special code "+semver: minor" (bumps minor) or "+semver: major" (bumps major)
- Merges from branches with feature/ (or feat/) prefix into develop increment minor, unless any of the commits in the merge contain "+semver: major" (bumps major)
- If first section of develop was not yet incremented compared to release, on new branch we should immediately see the increment
- On feature/ (or feat/) branches we should see minor increment, which takes precedence over patch increment
- Version on develop shows what the future release version will be, but if we have no new commits it should not change

## Example

- last commit on main is tagged as "2.1.0"; running givi on main outputs "2.1.0"
- develop branch was only just merged into main and had no new commits since; givi still outputs "2.1.0-alpha.123"
- we add direct commit to develop - it now outputs "2.1.1-alpha.1"
- we add another direct commit - version is "2.1.1-alpha.2"
- we create branch bugfix/ABC-123-foo_bar; it has no new commits yet; version is "2.1.1-ABC-123-foo-bar.0"
- we make a direct commit; version is now "2.1.1-ABC-123-foo-bar.1"
- we merge bugfix/ABC-123-foo_bar into develop and switch to develop; version is "2.1.1-alpha.4" (merge commit also counts)
- we merge develop into main and switch to main; version is "2.1.1"
- we switch to develop; it has no extra commits; version is still "2.1.1-alpha.4"
- we add direct commit to develop - it now outputs "2.1.2-alpha.1"
- we create branch feature/cool-abc; it has no new commits yet; version is "2.2.0-cool-abc.0"
- we add 3 direct commits - it now outputs "2.2.0-cool-abc.3"
- we merge feature/cool-abc into develop and switch to develop; version is "2.2.0-alpha.5"
- we create branch bugfix/ABC-456; it has no new commits yet; version is "2.2.0-ABC-456.0"
- we add 2 direct commits - it now outputs "2.2.0-ABC-456.2"
- we merge bugfix/ABC-456 into develop and switch to develop; version is "2.2.0-alpha.8"
- we create branch feature/cool-xyz; it has no new commits yet; version is "2.2.0-cool-xyz.0"
- we add 2 direct commits - it now outputs "2.2.0-cool-xyz.2"
- we merge feature/cool-xyz into develop and switch to develop; version is "2.2.0-alpha.11"
- we merge develop into main and switch to main; version is "2.2.0"
