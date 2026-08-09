package attempt

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// leaves are every package of castor's this one is allowed to link, and each is a leaf in
// the sense that matters here: it starts no process, opens no socket and touches no file.
// media is the vocabulary (streams, renditions, containers, renderer capabilities and the
// operator's delivery preference), carriage and read are pure tables over that vocabulary,
// and watch judges measurements handed to it through ports it never opens.
var leaves = []string{
	"github.com/stupside/castor/internal/media",
	"github.com/stupside/castor/internal/cast/carriage",
	"github.com/stupside/castor/internal/cast/read",
	"github.com/stupside/castor/internal/cast/watch",
	"github.com/stupside/castor/internal/cast/attempt",
}

// TestTheSpineLinksNothingThatRunsAProcessOrOpensASocket is the compile-time proof of the
// claim this package's doc comment makes in prose: it performs no I/O, so every recovery
// path is a table-driven unit test with no ffmpeg, no network and no renderer.
//
// The claim was FALSE while nothing checked it, and by one identifier: naming the
// operator's delivery preference in the delivery driver put ffmpeg, the device adapters,
// rokuchannel, the replay and HLS servers, the spool and the source resolver in this
// package's import graph. Every one of them starts a process or binds a port, so a test
// here could have been made to run an encoder by accident and nothing would have said so.
//
// It is asserted over the whole transitive graph and not over this package's own import
// list, because that is where the property lives: an allowed package that grew an import
// of the delivery driver would leave every file here looking exactly the same.
//
// os/exec is named separately because it is the standard library's one way to run a
// process, and no leaf above may reach it either. Sockets are covered by the allowlist
// rather than by naming net/http: media models a stream's request headers with
// http.Header, so the package is in this graph as a type and can never be excluded by
// name.
func TestTheSpineLinksNothingThatRunsAProcessOrOpensASocket(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))

	for _, dep := range deps {
		if strings.HasPrefix(dep, "github.com/stupside/castor/") && !slices.Contains(leaves, dep) {
			t.Errorf("the spine links %s, so a table-driven test of a recovery path links whatever that package runs; keep the value it needs in a leaf instead",
				dep)
		}
		if dep == "os/exec" {
			t.Error("the spine links os/exec, so something it imports can run a process: the whole point of this layer is that a recovery is decided over values")
		}
	}
	for _, leaf := range leaves {
		if !slices.Contains(deps, leaf) {
			t.Errorf("%s is allowed here and is no longer linked at all, so this list is describing an import graph castor does not have", leaf)
		}
	}
}
