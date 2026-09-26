package ffmpeg

import (
	"strconv"
	"strings"
)

// Command is an argv plus the extra output pipes it routes to, counted from the argv itself.
type Command struct {
	Args       []string
	ExtraPipes int
}

// NewCommand counts the extra pipes an argv routes to.
func NewCommand(args []string) Command {
	pipes := 0
	for _, arg := range args {
		rest, ok := strings.CutPrefix(arg, "pipe:")
		if !ok {
			continue
		}
		if fd, err := strconv.Atoi(rest); err == nil && fd >= firstExtraFD {
			pipes = max(pipes, fd-firstExtraFD+1)
		}
	}
	return Command{Args: args, ExtraPipes: pipes}
}
