package bindings

import (
	"context"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.LogBatch](service.EventLogLines)
	application.RegisterEvent[service.LogStatus](service.EventLogState)
}

type LogService struct {
	svc *service.Service
}

func NewLogService(svc *service.Service) *LogService {
	return &LogService{svc: svc}
}

func (l *LogService) Start(ctx context.Context, src service.LogSource) (service.LogStatus, error) {
	return l.svc.StartLogs(ctx, src)
}

func (l *LogService) Stop(streamID string) error {
	return l.svc.StopLogs(streamID)
}

func (l *LogService) Statuses() []service.LogStatus {
	return l.svc.LogStatuses()
}
