package bindings

import (
	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.EventBatch](service.EventEventBatch)
	application.RegisterEvent[service.EventStatus](service.EventEventState)
}

type EventService struct {
	svc *service.Service
}

func NewEventService(svc *service.Service) *EventService {
	return &EventService{svc: svc}
}

func (e *EventService) Start(clusterID string) (service.EventStatus, error) {
	return e.svc.StartEvents(clusterID)
}

func (e *EventService) Stop(streamID string) error {
	return e.svc.StopEvents(streamID)
}
