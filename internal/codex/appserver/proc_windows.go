//go:build windows

package appserver

import (
	"os"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

func killProcessGroup(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}
