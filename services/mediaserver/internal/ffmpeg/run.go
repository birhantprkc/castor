package ffmpeg

import (
	"context"
	"io"
	"os/exec"
	"strings"
)

// runStderrBytes bounds what Run keeps of a tool's stderr.
const runStderrBytes = 8 << 10

// Run runs one ffmpeg tool to its end, and returns the tail of what it said on stderr.
func Run(ctx context.Context, path string, args []string, stdin io.Reader, stdout io.Writer) (said string, err error) {
	cmd := exec.CommandContext(ctx, path, args...)
	stderr := &byteTail{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err = cmd.Run()
	return strings.TrimSpace(string(stderr.buf)), err
}

// byteTail keeps the last runStderrBytes bytes written to it.
type byteTail struct{ buf []byte }

func (t *byteTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - runStderrBytes; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
	}
	return len(p), nil
}
