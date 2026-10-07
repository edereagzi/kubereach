package bindings

import (
	"context"
	"log"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

const (
	// EventUpdateAvailable announces a newer release the background check found; it carries the Update.
	EventUpdateAvailable = "update:available"
	// EventUpdateCheck asks the window's update dialog to check, from macOS's application menu.
	EventUpdateCheck = "update:check"
	// EventUpdateProgress is Wails' report of the download; it carries the bytes written and the total.
	EventUpdateProgress = updater.EventDownloadProgress
)

func init() {
	application.RegisterEvent[Update](EventUpdateAvailable)
	application.RegisterEvent[application.Void](EventUpdateCheck)
	application.RegisterEvent[updater.Progress](EventUpdateProgress)
}

// updateKey is the public half of the key that build/release/update-manifest.sh signs each release's files with.
// An update whose signature it does not verify is not installed.
var updateKey = []byte(`-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAOuxwdHldmUiYAeMjMQdRw6EwcFkas6HYTFUwRcHujqI=
-----END PUBLIC KEY-----
`)

// updaterConfig describes releases to Wails' updater through the update.json that each release publishes;
// GitHub serves latest/download from the newest release that is not a prerelease.
// Kubereach draws the update flow itself: Wails' window downloads as soon as it finds a release, and here
// checking, downloading and installing are each the user's step.
func updaterConfig(version string, key []byte) (updater.Config, error) {
	p, err := endpoint.New(endpoint.Config{URL: repoURL + "/releases/latest/download/update.json"})
	if err != nil {
		return updater.Config{}, err
	}
	return updater.Config{CurrentVersion: version, Providers: []updater.Provider{p}, PublicKey: key, Window: updater.WindowNone}, nil
}

// Update is a newer release than the running one.
type Update struct {
	Version string `json:"version"`
	// Notes are the release's section of CHANGELOG.md, in Markdown.
	Notes string `json:"notes"`
	// Size is the download's size in bytes.
	Size int64 `json:"size"`
}

// ServiceStartup checks for updates at launch and once a day while the app runs. A release it finds is only
// announced, as update:available; nothing is downloaded until the user asks.
// Timers stop while the machine sleeps, so the day is measured on the wall clock, looked at hourly.
func (a *AppService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	if a.updates {
		go func() {
			tick := time.NewTicker(time.Hour)
			defer tick.Stop()
			for {
				last := time.Now().Round(0)
				a.checkQuietly(ctx)
				for time.Now().Round(0).Sub(last) < 24*time.Hour {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
					}
				}
			}
		}()
	}
	return nil
}

// checkQuietly announces a newer release, and leaves alone an update the user is already going through.
func (a *AppService) checkQuietly(ctx context.Context) {
	switch a.app.Updater.State() {
	case updater.StateChecking, updater.StateDownloading, updater.StateVerifying, updater.StateInstalling, updater.StateReady:
		return
	}
	u, err := a.check(ctx)
	if err != nil {
		log.Print("update check: ", err)
		return
	}
	if u != nil {
		a.app.Event.Emit(EventUpdateAvailable, *u)
	}
}

// AvailableUpdate is the release the last check found, for a window that loads after it was announced.
func (a *AppService) AvailableUpdate() *Update {
	return a.found.Load()
}

// CheckForUpdates asks GitHub for a newer release; nil when this one is the latest.
func (a *AppService) CheckForUpdates() (*Update, error) {
	u, err := a.check(context.Background())
	if err != nil {
		log.Print("update check: ", err)
		return nil, service.UserError("Kubereach could not reach GitHub to check for updates", err)
	}
	return u, nil
}

func (a *AppService) check(ctx context.Context) (*Update, error) {
	rel, err := a.app.Updater.Check(ctx)
	if err != nil {
		return nil, err
	}
	var u *Update
	if rel != nil {
		u = &Update{Version: rel.Version, Notes: rel.Notes, Size: rel.Artifact.Size}
	}
	a.found.Store(u)
	return u, nil
}

// DownloadUpdate downloads the release the last check found and verifies it against updateKey. Its progress
// arrives as EventUpdateProgress.
func (a *AppService) DownloadUpdate() error {
	if err := a.app.Updater.DownloadAndInstall(context.Background()); err != nil {
		log.Print("update download: ", err)
		return service.UserError("The update could not be downloaded and verified", err)
	}
	return nil
}

// RestartToUpdate quits Kubereach, puts the downloaded release in its place and starts it.
func (a *AppService) RestartToUpdate() error {
	if err := a.app.Updater.Restart(context.Background()); err != nil {
		log.Print("update restart: ", err)
		return service.UserError("Kubereach could not restart to update", err)
	}
	return nil
}
