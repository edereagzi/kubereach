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
// The menu is rebuilt each time it opens, so it is always current and never changes under the cursor.
type Tray struct {
	app    *application.App
	svc    *service.Service
	window *application.WebviewWindow
	tray   *application.SystemTray
	mu     sync.Mutex
	menu   *application.Menu
}

func NewTray(app *application.App, svc *service.Service, window *application.WebviewWindow) *Tray {
	t := &Tray{app: app, svc: svc, window: window, tray: app.SystemTray.New()}
	t.tray.SetTooltip("Kubereach")
	if runtime.GOOS == "darwin" {
		t.tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		t.tray.SetIcon(trayIcon)
	}
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	t.tray.OnClick(t.open)
	t.tray.OnRightClick(t.open)
	t.rebuild()
	return t
}

func (t *Tray) open() {
	t.rebuild()
	t.tray.OpenMenu()
}

// rebuild creates the menu: every Cluster reachable through an active Route, or a direct one with an enabled forward,
// with its enabled forwards beneath it; a forward opens in the browser.
func (t *Tray) rebuild() {
	t.mu.Lock()
	defer t.mu.Unlock()
	cfg, _ := t.svc.LoadConfig()
	routeState := map[string]service.State{}
	for _, st := range t.svc.RouteStatuses() {
		if st.State != service.StateIdle && st.State != service.StateStopped {
			routeState[st.RouteID] = st.State
		}
	}

	menu := t.app.NewMenu()
	listed := false
	for _, c := range cfg.Clusters {
		var forwards []service.PortForward
		for _, pf := range cfg.Forwards {
			if pf.Enabled && pf.ClusterID == c.ID {
				forwards = append(forwards, pf)
			}
		}
		// Behind a Route the Cluster is reachable only while the Route is up; a direct Cluster is running when it forwards.
		state, viaRoute := routeState[c.RouteID]
		if c.RouteID != "" && !viaRoute || c.RouteID == "" && len(forwards) == 0 {
			continue
		}
		listed = true
		label := c.Name
		if viaRoute && state != service.StateConnected {
			label += "  – " + string(state)
		}
		menu.Add(label).SetEnabled(false)
		for _, pf := range forwards {
			url := fmt.Sprintf("http://localhost:%d", pf.LocalPort)
			menu.Add(fmt.Sprintf("    localhost:%d  %s/%s", pf.LocalPort, pf.Target.Namespace, pf.Target.Name)).
				OnClick(func(*application.Context) { _ = t.app.Browser.OpenURL(url) })
		}
	}
	if !listed {
		menu.Add("Nothing running").SetEnabled(false)
	}
	menu.AddSeparator()
	menu.Add("Open").OnClick(func(*application.Context) { t.window.Show().Focus() })
	menu.Add("Quit").SetAccelerator("CmdOrCtrl+Q").OnClick(func(*application.Context) { t.app.Quit() })

	old := t.menu
	t.menu = menu
	t.tray.SetMenu(menu)
	if old != nil {
		old.Destroy()
	}
}
