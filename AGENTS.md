this project contains tools that ease the use of `br` (beads_rust) in a fast-moving, highly-collaborative environnment.

always read the following skills before beginning work:
- br
- brynsk-go

## layout

- `cmd/beads-tools`: the single binary; wiring only.
- `internal/cli`: argument parsing and subcommand dispatch.
- `internal/beadsfile`: reads and writes `.beads/issues.jsonl`. Unchanged lines keep their exact bytes; never re-render a row nobody asked to change.
- `internal/merge`: the three-way merge behind the git merge driver.
- `internal/trailers`: `Closes:`/`Refs:` commit trailers, parsed by git itself.
- `internal/land`: closing issues when their work reaches the main branch (`land pr`, `land sweep`).
- `internal/hook`: the commit-msg hook.
- `internal/install`: wires the driver and hook into a repository.
- `internal/testrepo`: throwaway git + br repositories for tests.

## rules

- the tools never run br against a long-lived database. edits go through `br --no-db` on a scratch copy of `.beads/`, and only the intended rows are spliced back, with a byte-for-byte check that nothing else moved.
- the tools never change the operator's checkout. anything that commits does so in a throwaway worktree.
- tests use `internal/testrepo` and run every command with an explicit directory. never `cd` in tests or scripts.
- use `github.com/stretchr/testify/require` and mockery mocks (`make mocks`, pinned in `tools/go.mod`). never hand-roll mocks.
