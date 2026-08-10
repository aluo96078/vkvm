//go:build windows

package service

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/windows/svc"
)

// RunWindowsService adapts the same context-owned core worker to the Windows
// SCM. Installation/configuration is intentionally separate so an installer
// can set Automatic (Delayed Start) and failure-recovery actions with sc.exe
// or MSI policy rather than requiring the running binary to elevate itself.
func RunWindowsService(name string, worker Worker) error {
	if name == "" || worker == nil {
		return errors.New("service name and worker are required")
	}
	return svc.Run(name, scmService{worker: worker})
}

type scmService struct{ worker Worker }

func (s scmService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWorkerSafely(s.worker, ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						status <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
						return false, 1
					}
				case <-time.After(10 * time.Second):
					status <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
					return false, 1
				}
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				status <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}
