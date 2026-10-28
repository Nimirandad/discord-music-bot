//go:build windows

package audio

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

var ntdll = windows.NewLazySystemDLL("ntdll.dll")
var ntSuspendProcess = ntdll.NewProc("NtSuspendProcess")
var ntResumeProcess = ntdll.NewProc("NtResumeProcess")

type subprocessPauser struct {
	mu        sync.Mutex
	processes []*os.Process
	paused    bool
}

func newSubprocessPauser(processes ...*os.Process) *subprocessPauser {
	return &subprocessPauser{processes: processes}
}

func (p *subprocessPauser) SetPaused(paused bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused == paused {
		return nil
	}

	changed := make([]*os.Process, 0, len(p.processes))
	proc := ntResumeProcess
	rollbackProc := ntSuspendProcess
	if paused {
		proc = ntSuspendProcess
		rollbackProc = ntResumeProcess
	}
	for _, process := range p.processes {
		if process == nil {
			continue
		}
		handle, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, uint32(process.Pid))
		if err != nil {
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				continue
			}
			return fmt.Errorf("open media process %d: %w", process.Pid, err)
		}
		status, _, _ := proc.Call(uintptr(handle))
		closeErr := windows.CloseHandle(handle)
		if status != 0 {
			rollbackErr := p.changeProcesses(changed, rollbackProc)
			if rollbackErr != nil {
				return errors.Join(
					fmt.Errorf("change media process %d pause state: %w", process.Pid, syscall.Errno(status)),
					rollbackErr,
				)
			}
			return fmt.Errorf("change media process %d pause state: %w", process.Pid, syscall.Errno(status))
		}
		if closeErr != nil {
			rollbackErr := p.changeProcesses(append(changed, process), rollbackProc)
			if rollbackErr != nil {
				return errors.Join(fmt.Errorf("close media process handle: %w", closeErr), rollbackErr)
			}
			return fmt.Errorf("close media process handle: %w", closeErr)
		}
		changed = append(changed, process)
	}
	p.paused = paused
	return nil
}

func (p *subprocessPauser) changeProcesses(processes []*os.Process, proc *windows.LazyProc) error {
	var failures []error
	for _, process := range processes {
		handle, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, uint32(process.Pid))
		if err != nil {
			failures = append(failures, fmt.Errorf("open media process %d to restore state: %w", process.Pid, err))
			continue
		}
		status, _, _ := proc.Call(uintptr(handle))
		closeErr := windows.CloseHandle(handle)
		if status != 0 {
			failures = append(failures, fmt.Errorf("restore media process %d state: %w", process.Pid, syscall.Errno(status)))
		}
		if closeErr != nil {
			failures = append(failures, fmt.Errorf("close media process handle: %w", closeErr))
		}
	}
	return errors.Join(failures...)
}
