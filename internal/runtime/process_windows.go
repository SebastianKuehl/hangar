//go:build windows

package runtime

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"

	"github.com/shirou/gopsutil/v4/process"
)

func prepareDetachedCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
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
	return true
}

func terminateManagedProcess(pid int) error {
	return signalProcessTree(pid, false)
}

func forceKillManagedProcess(pid int) error {
	return signalProcessTree(pid, true)
}

func signalProcessTree(rootPID int, force bool) error {
	pids, err := processTreePIDs(int32(rootPID))
	if err != nil {
		if !processExists(rootPID) {
			return nil
		}
		return err
	}
	if len(pids) == 0 {
		if !processExists(rootPID) {
			return nil
		}
		pids = []int32{int32(rootPID)}
	}
	for i := len(pids) - 1; i >= 0; i-- {
		pid := pids[i]
		if !processExists(int(pid)) {
			continue
		}
		proc, err := process.NewProcess(pid)
		if err != nil {
			if !processExists(int(pid)) {
				continue
			}
			return err
		}
		if force {
			err = proc.Kill()
		} else {
			err = proc.Terminate()
		}
		if err != nil && processExists(int(pid)) {
			return err
		}
	}
	return nil
}

func processTreePIDs(rootPID int32) ([]int32, error) {
	root, err := process.NewProcess(rootPID)
	if err != nil {
		return nil, err
	}
	seen := map[int32]struct{}{rootPID: {}}
	queue := []*process.Process{root}
	order := []int32{rootPID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		children, err := current.Children()
		if err != nil {
			continue
		}
		for _, child := range children {
			if child == nil {
				continue
			}
			if _, ok := seen[child.Pid]; ok {
				continue
			}
			seen[child.Pid] = struct{}{}
			order = append(order, child.Pid)
			queue = append(queue, child)
		}
	}
	return order, nil
}
