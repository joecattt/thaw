# Contributing to thaw

Thanks for your interest. Here's how to get set up and submit changes.

## Setup

```bash
git clone https://github.com/joecattt/thaw.git
cd thaw
make build    # requires Go 1.21+
make test     # runs all tests
```

No C compiler needed — SQLite is pure Go via `modernc.org/sqlite`.

## Running locally

```bash
./thaw setup          # installs shell hooks
./thaw doctor         # verify everything works
./thaw freeze         # take a test snapshot
./thaw status         # see what was captured
```

## Code structure

```
cmd/thaw/main.go      All CLI commands (Cobra)
internal/             One package per concern
  capture/            Parallel capture engine
  snapshot/           SQLite storage
  restore/            tmux reconstruction
  config/             TOML config + validation
  ...                 35+ packages
pkg/models/           Shared data types
```

## Making changes

1. Fork and create a feature branch
2. Make small, focused commits
3. Add or update tests for any logic changes
4. Run `make vet` and `make test` before pushing
5. Open a PR with a clear description of what and why

## Style

- `go vet` and `gofmt` must pass
- One package per directory, one concern per package
- Functions under 50 lines when possible
- Error messages start lowercase, no period
- No exported functions without a doc comment

## Adding a new command

1. Write the command function in `cmd/thaw/main.go`
2. Register it in `main()` under `root.AddCommand()` or `admin.AddCommand()`
3. If it needs a new package, create `internal/yourpkg/yourpkg.go`
4. Add the import to `cmd/thaw/main.go`

## Tests

```bash
make test             # all tests
go test ./internal/snapshot/ -v    # single package
go test -run TestRestore ./...     # single test
```

## Releases

Tags trigger CI builds. To release:

```bash
git tag v3.4.0
git push origin v3.4.0
```

GitHub Actions builds binaries for macOS (arm64/amd64) and Linux (amd64).

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
