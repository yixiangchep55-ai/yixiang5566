//go:build desktopapp && windows
// +build desktopapp,windows

package dashboard

import (
	"os/exec"
	"syscall"
)

func prepareNodeCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
