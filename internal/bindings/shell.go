package bindings

import (
	"context"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.ShellOutput](service.EventShellOutput)
	application.RegisterEvent[service.ShellStatus](service.EventShellState)
}

type ShellService struct {
	svc *service.Service
}

func NewShellService(svc *service.Service) *ShellService {
	return &ShellService{svc: svc}
}

func (sh *ShellService) Start(ctx context.Context, target service.ShellTarget, cols, rows int) (service.ShellStatus, error) {
	return sh.svc.StartShell(ctx, target, cols, rows)
}

func (sh *ShellService) Write(sessionID string, data string) error {
	return sh.svc.WriteShell(sessionID, []byte(data))
}

func (sh *ShellService) Resize(sessionID string, cols, rows int) error {
	return sh.svc.ResizeShell(sessionID, cols, rows)
}

func (sh *ShellService) Stop(sessionID string) error {
	return sh.svc.StopShell(sessionID)
}

func (sh *ShellService) Statuses() []service.ShellStatus {
	return sh.svc.ShellStatuses()
}
