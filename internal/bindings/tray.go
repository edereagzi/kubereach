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

// EventOpenCluster asks the window to show a Cluster's Overview; it carries the Cluster's ID.
const EventOpenCluster = "tray:open-cluster"

func init() {
	application.RegisterEvent[application.Void](service.EventConfigChanged)
	application.RegisterEvent[string](EventOpenCluster)
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

// ShowWindow brings a hidden or minimised window back to the front.
func ShowWindow(w application.Window) {
	w.UnMinimise()
	w.Show().Focus()
}

func (t *Tray) open() {
	t.rebuild()
	t.tray.OpenMenu()
}

// rebuild creates the menu: every Cluster, with its Route's state when that is not connected and its enabled forwards beneath it;
// a Cluster opens its Overview and a forward can be opened in the browser or have its address copied.
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
	forwardState := map[string]service.State{}
	for _, st := range t.svc.ForwardStatuses() {
		forwardState[st.Forward.ID] = st.State
	}

	menu := t.app.NewMenu()
	for _, c := range cfg.Clusters {
		var forwards []service.PortForward
		for _, pf := range cfg.Forwards {
			if pf.Enabled && pf.ClusterID == c.ID {
				forwards = append(forwards, pf)
			}
		}
		// A Cluster behind a Route says so only while the Route is not connected; a direct one is always reachable.
		label := c.Name
		if c.RouteID != "" {
			if state, up := routeState[c.RouteID]; !up {
				label += "  – not connected"
			} else if state != service.StateConnected {
				label += "  – " + string(state)
			}
		}
		menu.Add(label).OnClick(func(*application.Context) {
			ShowWindow(t.window)
			t.app.Event.Emit(EventOpenCluster, c.ID)
		})
		for _, pf := range forwards {
			address := fmt.Sprintf("localhost:%d", pf.LocalPort)
			scheme := "http"
			if pf.RemotePort == 443 || pf.RemotePort == 8443 {
				scheme = "https"
			}
			label := fmt.Sprintf("    %s  %s/%s", address, pf.Target.Namespace, pf.Target.Name)
			if forwardState[pf.ID] == service.StateError {
				label += "  – error"
			}
			sub := menu.AddSubmenu(label)
			sub.Add("Open in browser").OnClick(func(*application.Context) { _ = t.app.Browser.OpenURL(scheme + "://" + address) })
			sub.Add("Copy address").OnClick(func(*application.Context) { t.app.Clipboard.SetText(address) })
		}
	}
	if len(cfg.Clusters) == 0 {
		menu.Add("No clusters added").SetEnabled(false)
	}
	menu.AddSeparator()
	menu.Add("Open").OnClick(func(*application.Context) { ShowWindow(t.window) })
	menu.Add("Quit").SetAccelerator("CmdOrCtrl+Q").OnClick(func(*application.Context) { t.app.Quit() })

	old := t.menu
	t.menu = menu
	t.tray.SetMenu(menu)
	if old != nil {
		old.Destroy()
	}
}
