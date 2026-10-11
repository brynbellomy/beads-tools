# beads-tools

The git side of [br (beads_rust)](https://github.com/Dicklesworthstone/beads_rust).

br deliberately never runs git: it keeps issues in SQLite and exports them to
`.beads/issues.jsonl`, and leaves merging, committing and closing-on-merge to
you. With several people and agents working on branches at once, that leaves
three gaps:

- **Merges conflict on unrelated changes.** Each issue is one JSONL line, so a
  status change on one branch and a comment on another touch the same line and
  conflict, even though they never touch the same field.
- **IDs collide.** Child IDs (`<parent>.N`) and comment IDs come from each
  database's own counter, so two branches mint the same "next" number.
- **Issues stay open after their work merges.** Closing is done by hand on a
  branch, but the work is only done once it merges, often much later.

`beads-tools` fills them with one binary:

| Command | What it does |
|---|---|
| `beads-tools install` | Configures the repository: merge driver, `.gitattributes`, commit-msg hook. |
| `beads-tools merge` | The git merge driver for `.beads/issues.jsonl` (git runs it). |
| `beads-tools hook commit-msg` | Checks a commit's `Closes:`/`Refs:` trailers (git runs it). |
| `beads-tools land pr N` | Closes the issues PR *N* closes, on its branch, then merges it. |
| `beads-tools land sweep` | Closes issues whose closing commits are already on the main branch. |
| `beads-tools check` | Applies `land pr`'s trailer rules to a range of commits, for CI. |

## Install

```sh
go install github.com/brynbellomy/beads-tools/cmd/beads-tools@latest
cd your-repo
beads-tools install        # commit the .gitattributes change it makes
```

`install` writes `.beads/issues.jsonl merge=beads` to `.gitattributes`, sets
`merge.beads.driver` in the repository's local git config, and installs a
`commit-msg` hook. It never overwrites a hook it did not write; it prints the
line to add instead. Every clone runs `install` once, since git config and
hooks are not versioned.

`land` needs `br` and the [GitHub CLI](https://cli.github.com/) (`gh`) on
`PATH`.

## The merge driver

The driver merges three versions of the issues file (merge base, ours, theirs)
issue by issue, field by field:

- An issue or field only one side changed takes that side's value.
- `comments`, `labels` and `dependencies` merge as sets: an element survives
  when both sides have it or one side added it, and goes when either side
  removed it. Comments are identified by author, time and text, not by ID.
- `status`, `closed_at` and `close_reason` resolve together, so a status never
  comes from one side and its close reason from the other.
- When both sides change the same field to different values, the side whose
  `updated_at` is newer wins and the decision is printed. Pass `--strict` in
  `merge.beads.driver` to make these merges stop as conflicts instead.
- When both sides create a different child under the same `<parent>.N`, the
  incoming one is renumbered to the next free `N` and every reference to it is
  rewritten.
- A comment whose ID is already used by a different comment gets a fresh ID.
  Comments both sides have keep ours' ID.
- Lines the merge did not change keep their exact bytes. Output is sorted by
  ID, as br exports it.

The driver refuses input with conflict markers, malformed lines or duplicate
IDs. On refusal git reports an ordinary conflict and leaves ours in place.

This covers what `br sync --merge` does not: that command reconciles a
database with a file and resolves an issue changed on both sides by keeping
one side's whole issue, which drops the other side's comments.

## Linking commits to issues

Commits name the issues they work on with trailers at the end of the message:

```
agent: record subscription CLI tool traffic in history

Closes: proj-k83ff.1
Refs: proj-zz1ha
```

- `Closes:` — the issue is done once this commit is on the main branch. The
  commit that finishes an issue carries it.
- `Refs:` — the issue is not finished by this commit. A PR whose commits
  only reference an issue leaves it open, and `land pr` asks about it.

Several IDs can share one trailer, separated by commas or spaces. Trailer keys
are case-insensitive.

The commit-msg hook rejects IDs that do not exist (an issue created in the
same commit counts). Off the main branch it also requires at least one
trailer, except for commits that change only `.beads/`, merges, and
`fixup!`/`squash!`/`amend!` commits. Set `BEADS_TOOLS_SKIP=1` to skip the
check for one commit.

## Closing issues when work lands

The association is the commits themselves, so nothing is tracked anywhere
else: an issue that is already closed is the watermark, and every run is
idempotent.

**`beads-tools land pr N`** is the normal path. It reads the `Closes:`
trailers on the PR's commits, adds one commit to the PR's branch that closes
those issues, pushes it (a plain push, which fails if anyone pushed
meanwhile) and merges the PR with `gh pr merge --match-head-commit`. The work
and its closes reach the main branch in the same merge.

An issue the PR's commits name only with `Refs:` would stay open after the
merge, so `land pr` asks for a decision instead of guessing. It lists each
one and refuses to land until you either pass `--close <id>` (repeatable),
which closes it in the same landing commit, or pass `--keep-open` to confirm
the PR finishes none of them. Epics are not asked about: every commit
references its epic. An issue already closed on the branch, or closed by a
`Closes:` trailer, needs no decision.

`land pr` applies the commit-msg hook's rules to every commit in the PR, so a
clone without the hook cannot land unlinked work: it refuses a PR with any
commit whose trailers name an issue that does not exist and, when
`beadstools.requireTrailers` is on, any commit that names no issue (merges,
reverts and `.beads/`-only commits are exempt). It lists the offending
commits; reword them, push, and land again. `--dry-run` reports the same
refusal.

**`beads-tools check`** applies the same rules without `gh` or `br`, so CI
can show a failing check on a PR before anyone merges it, including through
the web UI, where `land pr` never runs:

```sh
beads-tools check                                   # <remote>/<main>..HEAD
beads-tools check --base origin/main --head HEAD    # explicit range
```

It reads the issues file at `--head`, prints each offending commit and exits
1 when any commit fails the rules (2 on errors). On a GitHub pull request
the checked-out HEAD is a synthetic merge commit, which counts as a merge and
is exempt. It also prints a note for each issue the range references with
`Refs:` but does not close, without failing: a reminder to add `Closes:` to
the finishing commit before `land pr` asks.

**`beads-tools land sweep`** is the backstop for PRs merged some other way,
such as the GitHub web UI. It finds issues that commits on the main branch
close but that are still open there, closes them on a fresh branch, opens a
PR, merges it and deletes the branch. It also lists issues that recent
commits on the main branch reference with `Refs:` but that nothing closed
(`--since`, default two weeks): a PR merged without `land pr` never got
asked about them, so close the finished ones by hand.

Both accept `--dry-run` (print the plan, change nothing) and `--no-merge`
(push and open, but leave merging to a person).

Both skip issues whose closing commit was reverted (git's standard
`This reverts commit <sha>.` line), and each issue's close reason cites the
oldest commit that closes it.

### How closes are written

Issues are closed by `br close` itself, so br's validation and your workflow
rules in `.beads/config.yaml` still apply. br runs on a scratch copy of
`.beads/` in JSONL-only mode, never against a database. Because br's export
re-renders rows it was not asked to touch (comment IDs, timestamp formats),
only the rows of the issues being closed are spliced back into the file. The
tool then checks that nothing else changed, byte for byte, before it commits.

Closes always happen in a throwaway worktree, never in your checkout.

## Configuration

All optional, in git config:

| Key | Default | Meaning |
|---|---|---|
| `beadstools.remote` | `origin` | Remote to publish to. |
| `beadstools.mainBranch` | the remote's HEAD, else `main` | Branch that counts as merged. |
| `beadstools.mergeMethod` | `merge` | `merge`, `squash` or `rebase`. `sweep` reads trailers from the main branch's history, and git only parses trailers in a message's last paragraph, so a squash message that concatenates several commits keeps only the last one's. Prefer `merge`. |
| `beadstools.requireTrailers` | `true` | Require a trailer on commits off the main branch, in the hook and in `land pr`. |

## Working with br

- **Prefer hash IDs.** `br create --parent X` always mints `X.N` from a local
  counter. `br create` followed by `br dep add <child> X --type parent-child`
  keeps the parent link with a collision-free ID.
- **Edit issues on branches.** The main checkout should only import pulled
  changes (`br sync --import-only`), so stashes never carry issue edits around
  pulls.

## Development

```sh
make test       # go test ./...
make mocks      # regenerate mocks with the pinned mockery
```

Tests that exercise git and br run against throwaway repositories and skip
when `br` is not installed.
