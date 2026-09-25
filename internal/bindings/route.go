package bindings

import (
	"os"
	"path/filepath"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.RouteStatus](service.EventRouteState)
	application.RegisterEvent[service.HostKeyPrompt](service.EventHostKey)
}

type RouteService struct {
	svc *service.Service
}

func NewRouteService(svc *service.Service) *RouteService {
	return &RouteService{svc: svc}
}

func (r *RouteService) Save(route service.Route) (service.Route, error) {
	return r.svc.SaveRoute(route)
}

func (r *RouteService) Delete(routeID string) error {
	return r.svc.DeleteRoute(routeID)
}

func (r *RouteService) SetClusterRoute(clusterID, routeID string) error {
	return r.svc.SetClusterRoute(clusterID, routeID)
}

func (r *RouteService) Connect(routeID, secret string) error {
	return r.svc.ConnectRoute(routeID, secret)
}

func (r *RouteService) AnswerHostKey(routeID string, accept bool) error {
	return r.svc.AnswerHostKey(routeID, accept)
}

// ImportSSHConfig opens the native file picker in ~/.ssh and imports the chosen SSH config.
func (r *RouteService) ImportSSHConfig() ([]service.Route, error) {
	path, err := pickInSSHDir("Import SSH config")
	if err != nil || path == "" {
		return nil, err
	}
	return r.svc.ImportSSHConfig(path)
}

func (r *RouteService) Stop(routeID string) error {
	return r.svc.StopRoute(routeID)
}

func (r *RouteService) Statuses() []service.RouteStatus {
	return r.svc.RouteStatuses()
}

// PickKeyFile opens the native file picker in ~/.ssh and returns the chosen path, or "" when cancelled.
func (r *RouteService) PickKeyFile() (string, error) {
	return pickInSSHDir("Choose SSH key")
}

func pickInSSHDir(title string) (string, error) {
	home, _ := os.UserHomeDir()
	return pickFile(title, filepath.Join(home, ".ssh"))
}

// mainWindow is the app's only window. A file dialog attached to it is a sheet on macOS, and on Windows it is owned
// by the window, so it opens centred over it and blocks it.
func mainWindow() application.Window {
	if all := application.Get().Window.GetAll(); len(all) > 0 {
		return all[0]
	}
	return nil
}

// pickFile opens the native file picker in dir and returns the chosen path, or "" when cancelled.
func pickFile(title, dir string) (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle(title).
		AttachToWindow(mainWindow()).
		SetDirectory(dir).
		ShowHiddenFiles(true).
		PromptForSingleSelection()
}
