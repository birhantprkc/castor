# Contributing to Castor

Bug reports, device reports, and pull requests are welcome.

## Scope

Castor is a general-purpose caster. These are declined:

- Bundled or default source lists
- Adapters or scrapers for a specific streaming site
- Anything mainly for reaching content you have no right to, such as defeating DRM, paywalls, or geo-restrictions

Welcome: bug fixes, device support, transcoding and subtitle work, and extraction robustness.

## Building from source

The whisper bindings use cgo, so the first build compiles the library with cmake:

```sh
git submodule update --init --recursive   # first checkout only
make build                                # libwhisper.a (~1 min), then ./castor
```

For plain Go tooling, export the build environment once per shell:

```sh
eval "$(make env)"
go run . scan
go test ./...
```

With [direnv](https://direnv.net), the checked-in `.envrc` does this on `cd` after `direnv allow`.

## The rules

### Checked by CI

Breaking one of these turns `go test ./...` or the `lint` job red.

| Rule | Checked by |
| --- | --- |
| Both delivery mechanisms pass one conformance suite: where they are fetched, flagging a delivery nobody fetched, Close finishing its reads, a cancelled Wait stopping | `sink_conformance_test.go` |
| Every device family passes one suite: `AwaitEnd` keeps polling a playing device and returns the cast's reason, and the stated envelope holds the H.264 baseline | `devicetest`, from each family's tests |
| No unreachable code (`deadcode -test ./...`; `castorNativeLog` is the one exception, called only from C) | `lint` |
| `modernize`, `staticcheck`, `go vet` and `gofmt -s` report nothing | `lint` |

Lint tools are pinned, so a new release never turns an unrelated push red.

Tests exercise behaviour, never the shape of the code (import graphs, which call site passes a value). Mutation-test what you add: break the behaviour, see the test fail with a clear message, restore.

### Held in review

- **The core is agnostic to source and device.** Decisions read capabilities as data. Device families and source formats are listed only in the composition root; nothing else branches on a family or names a site.
- **The tree is sliced by feature.** A top-level package is a feature, the shared media vocabulary, a tool several features run, or the composition root. A package one feature uses lives inside it. Folders are named for what they do, not for a layer. One responsibility per package, one concept per file, one file per strategy.
- **Consumers declare narrow ports; the composition root binds them.** That keeps decisions testable without ffmpeg, a network, or a renderer. The subtitle burn-in is the exception, bound by the command layer because it is cgo.
- **The API server and its client never import each other.** Both speak the cast contract through its translation layer, and only the composition root knows both. Anything that reaches into the operator's network (discovery, renderer control) belongs to the client; the server reads, encodes, and serves renderers what they fetch from it.
- **Absence is a value.** An optional lookup returns `(T, bool)`, never a nil to remember to check. A caller that can't handle absence returns an error naming what was missing.
- **A comment is one short line, or nothing.** Only for what the code can't say: a reason, a constraint, a field failure.
- **Modern Go, no shims.** Go 1.26 idioms over hand-rolled equivalents, and no compatibility shims or dead fallback paths.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org): `type(scope)?: summary`, with `type` one of `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`. Mark a breaking change with `feat!:` or a `BREAKING CHANGE:` footer. The types drive the changelog and the version bump.

Enable the local check once:

```sh
make hooks   # points core.hooksPath at .githooks/
```

## Releases

[release-please](https://github.com/googleapis/release-please) keeps a release PR up to date from the commits on `main`. Merging it tags `vX.Y.Z` and publishes the binaries, the Docker image (`:latest`), and the Homebrew cask.

For a preview, run the **canary** workflow on any branch. It publishes `ghcr.io/stupside/castor:canary` and moves no stable pointer.
