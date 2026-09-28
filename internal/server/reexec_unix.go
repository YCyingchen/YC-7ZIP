//go:build !windows

package server

import (
	"os"
	"syscall"
)

// reexec replaces the current process image with the binary at path.
//
// 自更新之后不能只 os.Exit：裸二进制部署是由 deploy/start.sh 用 setsid --fork
// 拉起的，没有任何 supervisor 会把它重新拉起来，退出就等于服务停摆。
// exec 换掉进程映像则 PID 不变、监听 socket 在重新监听后恢复，
// 不需要外部有人管。
func reexec(path string) error {
	// 先关掉继承的监听 fd 之外的干扰：把 stdio 留着，方便日志继续落文件。
	return syscall.Exec(path, os.Args, os.Environ())
}
