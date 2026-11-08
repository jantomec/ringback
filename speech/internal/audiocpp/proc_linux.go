package audiocpp

import (
	"os/exec"
	"syscall"
)

// dieWithParent has the kernel kill the server if the worker dies, however it dies, so a
// server never outlives its worker and holds the GPU.
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
