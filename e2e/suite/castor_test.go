package suite

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/e2e/settings"
)

// binary is castor as a user runs it, one process per cast so no case shares its state with another.
type binary string

func (b binary) Cast(ctx context.Context, launch settings.Launch, args []string) ([]byte, error) {
	var out bytes.Buffer
	// --debug brings the engine's lines back through the watch, so a failing case shows why.
	cmd := exec.CommandContext(ctx, string(b), slices.Concat([]string{"--debug"}, launch.Flags, args)...)
	cmd.Dir, cmd.Env = launch.Dir, append(os.Environ(), launch.Env...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.Bytes(), err
}

const (
	// castTimeout bounds one cast, well past the longest verdict castor waits for (two stall windows).
	castTimeout = 8 * time.Minute
	// handOff is how long a hand-off may land after castor exits: a detached Cast LOAD is still in flight.
	handOff = 10 * time.Second
)

// castor is the binary TestMain builds, the same main package a user runs.
var castor binary

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "castor-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "castor")
	build := exec.Command("go", "build", "-o", bin, "github.com/stupside/castor")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building castor:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	castor = binary(bin)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
