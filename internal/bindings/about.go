package bindings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const repoURL = "https://github.com/edereagzi/kubereach"

// AppService is what the Settings menu needs about Kubereach itself.
type AppService struct {
	app        *application.App
	version    string
	configPath string
}

func NewAppService(app *application.App, version, configPath string) *AppService {
	return &AppService{app: app, version: version, configPath: configPath}
}

// ShowAbout opens the native About dialog.
func (a *AppService) ShowAbout() {
	d := a.app.Dialog.Info().SetTitle("Kubereach " + a.version).SetMessage("Configuration file:\n" + a.configPath)
	d.AddButton("Reveal").OnClick(a.reveal)
	d.AddButton("GitHub").OnClick(func() { _ = a.app.Browser.OpenURL(repoURL) })
	d.SetDefaultButton(d.AddButton("OK"))
	d.Show()
}

// reveal selects the file in the OS file manager, or opens its directory before the first save.
func (a *AppService) reveal() {
	if _, err := os.Stat(a.configPath); errors.Is(err, os.ErrNotExist) {
		_ = a.app.Env.OpenFileManager(filepath.Dir(a.configPath), false)
		return
	}
	_ = a.app.Env.OpenFileManager(a.configPath, true)
}

// InstallMenu installs the macOS application menu; Windows and Linux show no menu bar.
// The About role would open Cocoa's own panel, so About is a plain item wired to ShowAbout.
func InstallMenu(app *application.App, showAbout func()) {
	if runtime.GOOS != "darwin" {
		return
	}
	menu := app.Menu.New()
	appMenu := menu.AddSubmenu("Kubereach")
	appMenu.Add("About Kubereach").OnClick(func(*application.Context) { showAbout() })
	appMenu.AddSeparator()
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.UnHide)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Quit)
	menu.AddRole(application.EditMenu)
	menu.AddRole(application.WindowMenu)
	app.Menu.Set(menu)
}
