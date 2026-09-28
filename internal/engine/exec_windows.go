package engine

import (
	"context"
	"os/exec"
	"syscall"
)

// execCommand builds a 7-Zip invocation. It is a thin indirection so the
// engine has one place to hook platform specific process attributes.
func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Do not pop a console window on Windows when the host runs us as a
	// windowless service.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
