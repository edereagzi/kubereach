package bindings

import (
	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.TerminalOutput](service.EventTerminalOutput)
	application.RegisterEvent[service.TerminalStatus](service.EventTerminalState)
}

type TerminalService struct {
	svc *service.Service
}

func NewTerminalService(svc *service.Service) *TerminalService {
	return &TerminalService{svc: svc}
}

func (t *TerminalService) Start(clusterID string, cols, rows int) (service.TerminalStatus, error) {
	return t.svc.StartTerminal(clusterID, cols, rows)
}

func (t *TerminalService) Write(id string, data string) error {
	return t.svc.WriteTerminal(id, []byte(data))
}

func (t *TerminalService) Resize(id string, cols, rows int) error {
	return t.svc.ResizeTerminal(id, cols, rows)
}

func (t *TerminalService) Stop(id string) error {
	return t.svc.StopTerminal(id)
}

func (t *TerminalService) Tail(id string) (service.SessionTail, error) {
	return t.svc.TerminalTail(id)
}

func (t *TerminalService) Statuses() []service.TerminalStatus {
	return t.svc.TerminalStatuses()
}
