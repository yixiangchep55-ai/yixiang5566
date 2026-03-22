//go:build !explorerui

package uiembed

import "io/fs"

func DistFS() (fs.FS, error) {
	return nil, fs.ErrNotExist
}
