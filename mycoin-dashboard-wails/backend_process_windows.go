//go:build windows

package main

import "syscall"

func backendSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
