//go:build !linux

package audiocpp

import "os/exec"

// dieWithParent does nothing off Linux, where the speech node doesn't run; the package builds
// there for its tests.
func dieWithParent(cmd *exec.Cmd) {}
