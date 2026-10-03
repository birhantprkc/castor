//go:build !unix

package suite

import "os/exec"

func grouped(*exec.Cmd) {}

// killGroup kills cmd alone: outside unix there is no process group to take its children with it.
func killGroup(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
