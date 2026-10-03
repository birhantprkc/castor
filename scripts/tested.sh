#!/usr/bin/env bash
# Run the unit tests (e2e excluded) and fail on any skip, since a skipped test is coverage that left silently.
set -euo pipefail

go test -json $(go list ./... | grep -v /e2e/) | python3 scripts/tested.py
