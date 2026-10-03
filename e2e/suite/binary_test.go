package suite

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
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
	err := b.command(ctx, launch, args, &out).Run()
	return out.Bytes(), err
}

// command runs b in a process group of its own, so the browsers and encoders it starts die with it when ctx ends.
func (b binary) command(ctx context.Context, launch settings.Launch, args []string, out io.Writer) *exec.Cmd {
	// --debug brings the engine's lines back through the watch, so a failing case shows why.
	cmd := exec.CommandContext(ctx, string(b), slices.Concat([]string{"--debug"}, launch.Flags, args)...)
	cmd.Dir, cmd.Env = launch.Dir, append(os.Environ(), launch.Env...)
	cmd.Stdout, cmd.Stderr = out, out
	// Grandchildren holding the output pipe open must not keep Wait from returning.
	cmd.WaitDelay = 5 * time.Second
	grouped(cmd)
	cmd.Cancel = func() error {
		killGroup(cmd)
		return nil
	}
	return cmd
}

// The binaries TestMain builds, the same main packages a user runs; castor-api without cgo, as it ships.
var castor, castorAPI, castorMedia binary

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
	for name, built := range map[string]*binary{"castor": &castor, "castor-api": &castorAPI, "castor-media": &castorMedia} {
		bin := filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", bin, "github.com/stupside/castor/cmd/"+name)
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if name == "castor-api" {
			build.Env = append(os.Environ(), "CGO_ENABLED=0")
		}
		if err := build.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n", name, err)
			os.RemoveAll(dir)
			os.Exit(1)
		}
		*built = binary(bin)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
