# Algorithm

More technical details on how it works.

## Calculating version

- If no tags: account for all commits throught the common ancestry from the main root (root commit on main is treated as "0.1.0")
- If tag exist: take the nearest found tag as a base and stop further descent down the tree
- Only tags that are a bare release version are used as release boundaries. Tags are parsed with the STRICT semver parser after stripping a single optional leading "v", so "1.2.3", "v1.2.3", "1.2.3+build", and valid CalVer without leading zeros like "2020.12.15" are accepted, while non-strict forms — partial versions ("v2", "2.1") and leading-zero segments ("01.2.3", "2020.01.15") — are ignored, as is any non-semver tag or a prerelease tag without a trailing counter (e.g. "1.2.3-rc")
- A prerelease tag that carries a trailing numeric counter (e.g. "4.5.6-foobar-x.3") is a "reference point", not a boundary: on a non-main branch it pins the in-progress version. On the tagged commit the version is the tag verbatim ("4.5.6-foobar-x.3"); the next commit continues the counter with the tag's label ("4.5.6-foobar-x.4"). When merged to develop it becomes "4.5.6-alpha.\<count\>", and when released to main it becomes "4.5.6".
- A reference tag acts as a downward-capable ANCHOR, not merely a raise-only floor: the anchored core (the tag's core, lifted by any explicit "+semver:" directive or feature merge that lands after the tag) REPLACES the normally-computed core when it is at least as high as the base release boundary the section builds on; if it is below that boundary it is ignored. So a reference tag can pull the core down as well as up, but never below the release it builds on. Plain commits after the tag only advance the counter — only an explicit "+semver:" directive or a feature merge after the tag lifts the anchor. When several reference tags are in range, the one nearest the tip wins, with ties broken toward the higher core.
- On a non-develop/non-main branch a reference tag caps the branch only when it sits on the branch's own first-parent line; a tag that arrived via a merge is raise-only there.
- If a single commit carries tags that resolve to different versions (e.g. "1.0.0" and "4.0.0", or a release and a prerelease reference), it is ambiguous. This is an error only when that commit is actually relevant to the computed version; a conflict on an older commit superseded by a later clean tag is ignored. Tags that resolve to the same version (e.g. "1.2.3" and "v1.2.3", or "1.2.3" and "1.2.3+build") are not a conflict.
- Versions on main branch must always be release ones (e.g. "0.1.0", "3.4.5", etc.)
- Versions on "develop" branch must always have a.b.c-alpha.xy format, e.g. "2.3.0-alpha.43"
- Versions on other branches are based on branch name, ignoring branch type prefix (part before "/"); e.g. on branch "bugfix/debug_bootstrap" the version may be "0.57.0-debug-bootstrap.42"
- Whenever major, minor, or patch version is incremented on non-main branch, the counter at the end resets
- On develop, counter resets to 1. Direct commits increment it by 1. Merge commits - by the number of commits inside the merge.
- On other branches: while it has no extra commits compared to develop, the counter is 0. Direct commits increment it by 1. Merge commits - by the number of commits inside the merge.
- A branch is versioned relative to its fork point on develop's mainline, so once it is merged back into develop its own commits still count (its version stays stable) until the branch advances
- A merge's nature (feature/bugfix, or any "+semver:" directive) is derived solely from the merge commit's MESSAGE, never from a branch ref, so deleting a merged branch afterwards (as real git-flow does with short-lived branches, or even with develop after a release) never changes any computed version
- A feature merge into develop normally contributes a minor, but if the merged-in branch's tip (the merge's second parent) carries a reference tag that capped it to patch, the feature merge inherits that patch decision (contributing no explicit bump, so only the patch floor applies); a reference tag deeper inside the branch than its tip does not cap it, since the branch's own later work reasserts the feature minor
- If develop is merged INTO a feature/bugfix/etc. branch, the branch's fork point advances up develop's mainline to the merged develop tip, so it inherits develop's accumulated bump: a patch-only develop keeps the branch's core unchanged (only the trailing counter advances), while a develop that had reason to bump minor or major raises the branch's increment to minor or major accordingly (feature branches keep their minor floor)
- Direct commits on main or develop increment patch, unless commit message contains special code "+semver: minor" (bumps minor) or "+semver: major" (bumps major)
- Merges from branches with feature/ (or feat/) prefix into develop increment minor, unless any of the inner commits of the merged branch contain "+semver: major" (bumps major); this is distinct from a "+semver:" directive on the merge commit's OWN message, described next
- A "+semver:" directive is also honored on a MERGE commit's own message (a direct merge into main, a develop -> main release merge, or a branch merged into develop), where several directives in one message resolve to the strongest one (patch < minor < major), order-independently
- On a merge commit an explicit "+semver: \<level\>" directive is EXACT: the merge is worth exactly that level — both a floor and a ceiling. It overrides everything the merge brought in — the automatic feature-minor, any inner "+semver:" bumps, and any reference-tag anchor — both up and down. So a "+semver: patch" merge over an inner "+semver: major" yields only a patch, and a "+semver: major" merge over a plain bugfix yields a major.
- The ceiling a "+semver:" merge imposes applies to every commit it introduced (those reachable from its second parent but not its first) at min(directive, any outer ceiling). Ceilings compose through nested capping merges (the lowest wins along a path), but a commit also reachable by an independent, un-capped path keeps its full weight. The commit COUNT is never affected — every commit in range is still counted once.
- If first section of develop was not yet incremented compared to release, on new branch we should immediately see the increment
- On feature/ (or feat/) branches we should see minor increment, which takes precedence over patch increment
- On any other short-lived branch (bugfix/hotfix/etc.) we should immediately see a patch increment, even before its first commit; this floor never double-bumps — it only applies when the core was not already advanced past the last release (by accumulated integration-section work or the branch's own commits)
- Version on develop shows what the future release version will be, but if we have no new commits it should not change

## Resolving reference branches (local vs remote)

The reference branches the algorithm consults — `main`/`master` and `develop` — are resolved as follows (this only affects those refs; HEAD is always used as-is):

- Only a local branch exists: use it.
- Only a remote-tracking ref exists (the fresh-clone / CI layout, where just the checked-out branch has a local head): use the remote. The remote configured as the upstream of the branch being computed (`branch.<branch>.remote`) is preferred, then `origin`, then a unique match across remotes; matching several remotes is an error (ambiguous).
- Both exist: the REMOTE wins, but only when the local head is not ahead of it (every local commit is reachable from the remote tip) AND they share a common merge base — this adopts a more up-to-date remote when local merely lags. If local has commits the remote lacks (local is ahead or diverged), or the histories are unrelated (no merge base), the LOCAL head wins, so local work is never silently discarded.
- For the permanent release branch, the NAME preference (`main` over `master`) is applied first, and only then is each name resolved local-or-remote. So `main` (whether local or a remote-tracking ref) is chosen before `master` is ever consulted — e.g. a remote-only `main` wins over a local `master`.

Every resolution decision is reported under `--debug`.

## Example

- last commit on main is tagged as "2.1.0"; running genver on main outputs "2.1.0"
- develop branch was only just merged into main and had no new commits since; genver still outputs "2.1.0-alpha.123"
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

### More examples

Refer to unit tests in this repo.
