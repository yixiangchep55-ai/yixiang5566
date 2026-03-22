//go:build desktopapp && !windows
// +build desktopapp,!windows

package dashboard

import "os/exec"

func prepareNodeCommand(cmd *exec.Cmd) {}
