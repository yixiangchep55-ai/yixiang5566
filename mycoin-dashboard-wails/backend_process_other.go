//go:build !windows

package main

import "syscall"

func backendSysProcAttr() *syscall.SysProcAttr {
	return nil
}
