package bindings

import (
	"context"
	"os"

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

// Save asks where to save and writes text there; "" when cancelled. The text is the panel's rendering, so the service has
// nothing to add and the file is written here.
func (l *LogService) Save(name, text string) (string, error) {
	path, err := promptSave("Save logs", name+".log")
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, []byte(text), 0o644)
}
