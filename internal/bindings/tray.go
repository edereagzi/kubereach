package bindings

import (
	_ "embed"
	"fmt"
	"runtime"
	"sync"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func init() {
	application.RegisterEvent[application.Void](service.EventConfigChanged)
}

// trayIcon is the coloured mark; trayTemplateIcon is its monochrome form, which macOS tints to match the menu bar.
var (
	//go:embed tray.png
	trayIcon []byte
	//go:embed tray-template.png
	trayTemplateIcon []byte
)

// Tray keeps the system tray menu in step with the service; closing the window only hides it.
type Tray struct {
	app     *application.App
	svc     *service.Service
	window  *application.WebviewWindow
	tray    *application.SystemTray
	mu      sync.Mutex
	menu    *application.Menu
	version string
}

func NewTray(app *application.App, svc *service.Service, window *application.WebviewWindow, version string) *Tray {
	t := &Tray{app: app, svc: svc, window: window, tray: app.SystemTray.New(), version: version}
	if runtime.GOOS == "darwin" {
		t.tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		t.tray.SetIcon(trayIcon)
	}
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	for _, name := range []string{service.EventRouteState, service.EventForwardState, service.EventConfigChanged} {
		app.Event.On(name, func(*application.CustomEvent) { t.Refresh() })
	}
	t.Refresh()
	return t
}

var trayLabels = map[service.State]string{
	service.StateIdle:         "Nothing connected",
	service.StateConnected:    "Connected",
	service.StateConnecting:   "Connecting",
	service.StateReconnecting: "Reconnecting",
	service.StateError:        "Error",
}

// Refresh rebuilds the menu from the current state and Saved Forwards.
func (t *Tray) Refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.svc.OverallState()
	t.tray.SetTooltip("Kubereach: " + trayLabels[state])

	menu := t.app.NewMenu()
	menu.Add(trayLabels[state]).SetEnabled(false)
	menu.AddSeparator()
	cfg, _ := t.svc.LoadConfig()
	bound := map[string]service.ForwardStatus{}
	for _, st := range t.svc.ForwardStatuses() {
		bound[st.Forward.ID] = st
	}
	listed := false
	for _, c := range cfg.Clusters {
		var sub *application.Menu
		for _, pf := range cfg.Forwards {
			if pf.ClusterID != c.ID {
				continue
			}
			if sub == nil {
				sub = menu.AddSubmenu(c.Name)
				listed = true
			}
			label := fmt.Sprintf("%s/%s:%d → localhost:%d", pf.Target.Namespace, pf.Target.Name, pf.RemotePort, pf.LocalPort)
			if st, ok := bound[pf.ID]; ok && st.State != service.StateIdle {
				label += " (" + string(st.State) + ")"
			}
			sub.AddCheckbox(label, pf.Enabled).OnClick(func(*application.Context) { go t.report(t.svc.SetForwardEnabled(pf.ID, !pf.Enabled)) })
		}
	}
	if listed {
		menu.AddSeparator()
	}
	menu.Add("Kubereach " + t.version).SetEnabled(false)
	menu.Add("Show Window").OnClick(func(*application.Context) { t.window.Show().Focus() })
	menu.Add("Quit").OnClick(func(*application.Context) { t.app.Quit() })

	old := t.menu
	t.menu = menu
	t.tray.SetMenu(menu)
	if old != nil {
		old.Destroy()
	}
}

func (t *Tray) report(err error) {
	if err != nil {
		t.app.Dialog.Error().SetTitle("Kubereach").SetMessage(err.Error()).Show()
	}
}
