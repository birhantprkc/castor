#!/usr/bin/env bash
# Run the test suite and refuse to call a suite passed that never ran.
# Run from the repo root, in CI, under `eval "$(make env)"`.
set -euo pipefail

# t.Skip is green, so a suite that never ran looks exactly like one that passed, and
# this job passed for a long time having run none of the real-encode tests: the
# served-path engine test, every delivery encode, the gate tests, encoder selection,
# the ranker's playlist fixtures. ffmpeg is not on the runner
# image, every one of those tests is gated on finding it, and nothing said a word.
# The setup action installs the tools now; this refuses to go green if they vanish.
#
# The check is "nothing skipped" rather than a list of test names, because a list
# dates the moment somebody adds a test and a skip is the thing that hides. A new
# skip for a new reason fails here too, which is right: it should be a decision.
#
# internal/subtitle/whisper is the one exemption and it is honest, not a hole: its
# transcriber test needs a model weighing hundreds of megabytes that no CI run
# downloads, so that skip states a resource CI genuinely does not have.
# e2e is left out of CI.
go test -json $(go list ./... | grep -v /e2e/) | python3 scripts/tested.py
