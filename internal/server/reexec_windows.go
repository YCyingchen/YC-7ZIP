//go:build windows

package server

import "errors"

// reexec is not available on Windows: there is no exec-family call that
// replaces the process image, so the only correct move is to hand the problem
// to whoever started us. Windows is not a supported deployment target for
// YC-7ZIP (the shipped targets are Linux/NAS), so this is a guard rather than
// a feature gap.
func reexec(string) error {
	return errors.New("Windows 上无法原地重启进程：请手动重启服务")
}
