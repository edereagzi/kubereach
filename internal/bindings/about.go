package bindings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const repoURL = "https://github.com/edereagzi/kubereach"

// AppService holds what the About dialog shows about Kubereach itself.
// macOS shows the native dialog from its application menu; Windows and Linux draw it in the frontend from Info,
// since the Windows message box has no buttons but OK.
type AppService struct {
	app        *application.App
	version    string
	configPath string
}

func NewAppService(app *application.App, version, configPath string) *AppService {
	return &AppService{app: app, version: version, configPath: configPath}
}

// AboutInfo is what the About dialog shows.
type AboutInfo struct {
	Version    string `json:"version"`
	ConfigPath string `json:"configPath"`
	RepoURL    string `json:"repoURL"`
}

// Info is what the frontend's About dialog shows on Windows and Linux.
func (a *AppService) Info() AboutInfo {
	return AboutInfo{Version: a.version, ConfigPath: a.configPath, RepoURL: repoURL}
}

// showAbout opens the native About dialog.
func (a *AppService) showAbout() {
	d := a.app.Dialog.Info().SetTitle("Kubereach " + a.version).SetMessage("Configuration file:\n" + a.configPath)
	d.AddButton("Reveal").OnClick(a.Reveal)
	d.AddButton("GitHub").OnClick(func() { _ = a.app.Browser.OpenURL(repoURL) })
	d.SetDefaultButton(d.AddButton("OK"))
	d.Show()
}

// Reveal selects the configuration file in the OS file manager, or opens its directory before the first save.
func (a *AppService) Reveal() {
	if _, err := os.Stat(a.configPath); errors.Is(err, os.ErrNotExist) {
		_ = a.app.Env.OpenFileManager(filepath.Dir(a.configPath), false)
		return
	}
	_ = a.app.Env.OpenFileManager(a.configPath, true)
}

// InstallMenu installs the macOS application menu. Windows and Linux show no menu bar, so Ctrl+Q quits there;
// without a tray it is the only way to.
// The About role would open Cocoa's own panel, so About is a plain item wired to showAbout.
func InstallMenu(app *application.App, about *AppService) {
	if runtime.GOOS != "darwin" {
		// App key bindings run inside the webview's key handler; quitting there tears the window down under it.
		app.KeyBinding.Add("Ctrl+Q", func(application.Window) { go app.Quit() })
		return
	}
	menu := app.Menu.New()
	appMenu := menu.AddSubmenu("Kubereach")
	appMenu.Add("About Kubereach").OnClick(func(*application.Context) { about.showAbout() })
	appMenu.AddSeparator()
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.UnHide)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Quit)
	menu.AddRole(application.FileMenu)
	menu.AddRole(application.EditMenu)
	menu.AddRole(application.ViewMenu)
	menu.AddRole(application.WindowMenu)
	app.Menu.Set(menu)
}
