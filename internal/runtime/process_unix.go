//go:build !windows

package runtime

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"

	"github.com/shirou/gopsutil/v4/process"
)

func prepareDetachedCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processExists(pid int) bool {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return false
	}
	statuses, err := proc.Status()
	if err == nil {
		for _, status := range statuses {
			if strings.Contains(strings.ToLower(status), "zombie") {
				return false
			}
		}
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateManagedProcess(pid int) error {
	return signalManagedProcess(pid, syscall.SIGTERM)
}

func forceKillManagedProcess(pid int) error {
	return signalManagedProcess(pid, syscall.SIGKILL)
}

func signalManagedProcess(pid int, sig syscall.Signal) error {
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid > 0 {
		if err := syscall.Kill(-pgid, sig); err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		}
	}
	err = syscall.Kill(pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
