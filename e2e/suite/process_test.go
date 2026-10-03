//go:build unix

package suite

import (
	"os/exec"
	"syscall"
)

// grouped starts cmd in a process group of its own, so the browsers and encoders it runs die with it.
func grouped(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills cmd's process group, whatever of it is still running.
func killGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
