//go:build linux

package audio

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

type subprocessPauser struct {
	processes []*os.Process
}

func newSubprocessPauser(processes ...*os.Process) *subprocessPauser {
	return &subprocessPauser{processes: processes}
}

func (p *subprocessPauser) SetPaused(paused bool) error {
	signal := syscall.SIGCONT
	if paused {
		signal = syscall.SIGSTOP
	}

	signaled := make([]*os.Process, 0, len(p.processes))
	for _, process := range p.processes {
		if process == nil {
			continue
		}
		if err := process.Signal(signal); err != nil {
			if errors.Is(err, os.ErrProcessDone) {
				continue
			}
			rollback := syscall.SIGCONT
			if !paused {
				rollback = syscall.SIGSTOP
			}
			for _, prior := range signaled {
				if rollbackErr := prior.Signal(rollback); rollbackErr != nil && !errors.Is(rollbackErr, os.ErrProcessDone) {
					return errors.Join(
						fmt.Errorf("signal media process %d: %w", process.Pid, err),
						fmt.Errorf("restore media process %d state: %w", prior.Pid, rollbackErr),
					)
				}
			}
			return fmt.Errorf("signal media process %d: %w", process.Pid, err)
		}
		signaled = append(signaled, process)
	}
	return nil
}
