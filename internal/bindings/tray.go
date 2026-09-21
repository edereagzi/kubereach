package bindings

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func init() {
	application.RegisterEvent[application.Void](service.EventConfigChanged)
}

// Tray keeps the system tray icon and menu in step with the service; closing the window only hides it.
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

// Refresh rebuilds the icon and menu from the current state and Saved Forwards.
func (t *Tray) Refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.svc.OverallState()
	t.tray.SetIcon(trayIcons[state])
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

// trayIcons are filled circles in the colour of each overall state, drawn once at startup.
var trayIcons = func() map[service.State][]byte {
	colours := map[service.State]color.RGBA{
		service.StateIdle:         {0x8e, 0x8e, 0x93, 0xff},
		service.StateConnected:    {0x34, 0xc7, 0x59, 0xff},
		service.StateReconnecting: {0xff, 0x9f, 0x0a, 0xff},
		service.StateError:        {0xff, 0x45, 0x3a, 0xff},
	}
	icons := map[service.State][]byte{}
	for state, c := range colours {
		icons[state] = circlePNG(32, c)
	}
	icons[service.StateConnecting] = icons[service.StateReconnecting]
	return icons
}()

func circlePNG(size int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	r := float64(size)/2 - 1
	for y := range size {
		for x := range size {
			dx, dy := float64(x)+0.5-float64(size)/2, float64(y)+0.5-float64(size)/2
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
