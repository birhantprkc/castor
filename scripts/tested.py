"""Reprint `go test -json` as humans read it, and fail on a skip that hides coverage.

Read by scripts/tested.sh, which explains why a skip is a failure here.
"""

import json
import sys

EXEMPT = "github.com/stupside/castor/internal/subtitle/whisper"

# The suite takes minutes once the media tools are present, and a redirected stdout
# is block buffered, so without this a CI log shows nothing at all until the run is
# over and a slow suite is indistinguishable from a hung one.
sys.stdout.reconfigure(line_buffering=True)

skipped = []
for line in sys.stdin:
    try:
        event = json.loads(line)
    except ValueError:
        sys.stdout.write(line)
        continue
    action = event.get("Action")
    # "build-output" and not just "output": a package that fails to COMPILE reports the compiler's
    # diagnostics under that action, and the test events that follow carry only "FAIL pkg [build
    # failed]". Dropping it turned every build break into a red run whose log said nothing at all.
    if action in ("output", "build-output"):
        sys.stdout.write(event.get("Output", ""))
    elif action == "skip" and event.get("Test") and event.get("Package") != EXEMPT:
        skipped.append(event["Package"] + "." + event["Test"])

if skipped:
    print("\nthese tests skipped, so the coverage they hold did not run:", file=sys.stderr)
    for name in skipped:
        print("  " + name, file=sys.stderr)
    print(
        "\nevery skip castor ships is gated on a media tool the setup action installs,\n"
        "so this means the tool went missing and the coverage silently left with it.",
        file=sys.stderr,
    )
    sys.exit(1)
