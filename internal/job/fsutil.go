package job

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// RemoveAll deletes a workspace tree.
//
// os.RemoveAll fails on Windows when it meets a read-only attribute, which is
// common for files a user uploaded with restrictive permissions. Walking the
// tree and clearing the flag first makes the delete reliable.
func RemoveAll(root string) error {
	if root == "" || root == "/" || root == "." {
		return errors.New("拒绝删除工作区根目录")
	}
	if runtime.GOOS != "windows" {
		return os.RemoveAll(root)
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chmod(p, 0o700)
		return nil
	})
	return os.RemoveAll(root)
}
