# Contributing to Castor

Bug reports, tested device reports, and pull requests are all welcome.

## Scope

Castor is a general-purpose caster and a proof of concept. To keep it that way,
some contributions are out of scope and will be declined:

- Bundled or default source lists (Castor ships none by design)
- Adapters or scrapers targeting a specific streaming site
- Anything whose main purpose is to access content you have no right to, such as
  defeating DRM, paywalls, or geo-restrictions

Welcome: bug fixes, new device support, transcoding and subtitle improvements,
and general robustness of the extraction pipeline.

## Building from source

The whisper bindings use cgo, so building requires a one-time cmake build of the linked library:

```sh
git submodule update --init --recursive   # first checkout only
make build                                # builds libwhisper.a (~1 min), then ./castor
```

To run without producing a binary, export the build environment once per shell and use plain Go tooling:

```sh
eval "$(make env)"
go run . scan          # discover devices, a quick check that the build runs
go test ./...
go vet ./...
```

With [direnv](https://direnv.net) installed, the checked-in `.envrc` exports the environment automatically on `cd`, so plain `go build`, `go run .`, and `go test ./...` just work after `direnv allow`.

## The rules

Castor holds a handful of architecture rules, and each one below says what proves it. The first table is mechanised: break one of these and `go test ./...` or the `lint` job goes red. The list after it is held by review alone.

### Mechanised

| Rule | What it says | Proved by |
| --- | --- | --- |
| A port with two implementations has one specification | Both delivery mechanisms, opened the way a cast opens them, answer to one set of assertions: where they are fetched, that a delivery nobody fetched is flagged and one the renderer took is not, that Close finishes reading before it returns, that a cancelled Wait stops. What each mechanism decides for itself (its idle rule) is deliberately not judged there. | `execute.TestEveryMechanism...` in `sink_conformance_test.go` |
| Every device family answers to one suite | A family's `AwaitEnd` keeps polling a playing device (the suite runs several poll intervals in a synctest bubble) and returns the cast's reason when it ends, and the envelope it states holds the universal H.264 baseline (and, where it asks the device nothing, no model-specific codec). | `devicetest`, called from each family's own tests |
| Nothing ships that nothing reaches | `deadcode -test ./...` reports no unreachable function. The `-test` flag is load-bearing: without it everything only a test reaches reads as dead. | the `lint` job |
| The tree stays modern Go | `modernize` reports nothing over `./internal/...` and `./cmd/...`. | the `lint` job |
| staticcheck is clean | `staticcheck` reports nothing over `./internal/...` and `./cmd/...`. | the `lint` job |
| go vet is clean | `go vet ./...` reports nothing. | the `lint` job |
| Every file is `gofmt -s` clean | `gofmt -s -l cmd internal e2e main.go` prints nothing. | the `lint` job |

Notes on reading that table:

- `deadcode` allows exactly one line through, and the exception is written into the step: `castorNativeLog` carries `//export` and `nativelog.c` installs it as whisper.cpp's global log callback, so its only caller is C and no Go call graph can see it.
- Every lint tool is pinned to a version. A gate that changes under CI turns an unrelated push red and teaches everyone to ignore it.
- Tests exercise behaviour. A test whose subject is the shape of the code (an import graph, which call site supplies a value, whether a table has a row per name) is not written here.
- The suites are worth what breaking them proves. Anything added to one should be mutation tested: break the behaviour, confirm the test fails and says which property died, restore, confirm it passes. A test that passes against a broken implementation is worse than no test, because it manufactures confidence.

### Held by review

These are real and nothing checks them. They are yours to hold in review.

- **The core is agnostic to where a cast comes from and where it goes.** A decision reads capabilities as data. Device families and source formats are listed only in the composition root, which attaches each family's typed section from `config.yaml`: no decision, table or policy anywhere else branches on a device family, and nothing in the tree names a streaming site.
- **The tree is sliced by feature.** A top-level package is a feature, the shared media vocabulary, a tool several features run, or the composition root. A package only one feature uses lives inside that feature, and a folder is named for what it does, never for a layer. A package holds one responsibility, a file one concept, and a strategy has a file of its own.
- **A consumer declares the narrow port it needs, and a composition root binds it.** Each package declares only the methods it drives (`source.Format` and `device.Family` are the strategies a format or a family implements), so the judgement stays exercisable with no ffmpeg, no network and no renderer. The composition root binds every port but the subtitle burn-in, which the command layer binds because it is the one cgo mechanism the root may not name.
- **Absence is a value.** An optional lookup returns `(T, bool)`, never a nil the caller has to remember to check: `media.Program.PrimaryInput`, `compose.Compose`, `container.FormatForContentType`. A caller that cannot handle absence should say so where it happens, with an error naming what was missing, rather than dereference and fail five steps later.
- **A comment is one short line, or it is not there.** Write one only for what the code cannot say: a reason, a constraint, a field failure. Never restate a name or narrate the next line.

## Commit messages

Castor uses [Conventional Commits](https://www.conventionalcommits.org). Each subject is `type(scope)?: summary`, where `type` is one of `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`. Use `feat!:` (or a `BREAKING CHANGE:` footer) for a breaking change. These types drive the changelog and the next version bump, so they are not cosmetic.

Enable the local check once; it rejects non-conforming messages before they land, and needs nothing but git and bash (no tools to install):

```sh
make hooks   # points core.hooksPath at .githooks/
```

## Releases

Releases are automated with [release-please](https://github.com/googleapis/release-please). Merging Conventional Commits to `main` keeps a standing release PR updated with the next version and `CHANGELOG.md`; merging that PR tags `vX.Y.Z` and publishes the binaries, the Docker image (`:latest`), and the Homebrew cask.

For a bleeding-edge preview, run the **canary** workflow (Actions tab) on any branch. It publishes `ghcr.io/stupside/castor:canary` at `vX.Y.Z-canary.<sha>` and never moves a stable pointer.

## Notes

- Castor uses bleeding-edge Go (`go 1.26`): `errors.AsType` over `errors.As`, `sync.WaitGroup.Go`, `reflect.TypeFor`, the `slices`/`maps` packages, the `min`/`max`/`clear` builtins, range-over-int and range-over-func, generics. No hand-rolled equivalents. `modernize` runs in CI over `./internal/...` and `./cmd/...`, so most of this is now enforced rather than remembered.
- Don't add compatibility shims or dead fallback paths.
