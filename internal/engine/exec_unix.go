//go:build !windows

package engine

import (
	"context"
	"os/exec"
	"syscall"
)

// execCommand mirrors the Windows variant without the console-window flag.
func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Put 7-Zip in its own process group so a cancelled request can take down
	// the whole pipeline rather than leaving encoder children behind.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}
