package bindings

import (
	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.ForwardStatus](service.EventForwardState)
}

type ForwardService struct {
	svc *service.Service
}

func NewForwardService(svc *service.Service) *ForwardService {
	return &ForwardService{svc: svc}
}

func (f *ForwardService) Save(pf service.PortForward) (service.PortForward, error) {
	return f.svc.SaveForward(pf)
}

func (f *ForwardService) Delete(forwardID string) error {
	return f.svc.DeleteForward(forwardID)
}

func (f *ForwardService) SetEnabled(forwardID string, enabled bool) error {
	return f.svc.SetForwardEnabled(forwardID, enabled)
}

func (f *ForwardService) Statuses() []service.ForwardStatus {
	return f.svc.ForwardStatuses()
}
