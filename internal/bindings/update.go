package bindings

import (
	"context"
	_ "embed"
	"encoding/base64"
	"log"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// updateKey is the public half of the key that build/release/update-manifest.sh signs each release's files with.
// An update whose signature it does not verify is not installed.
var updateKey = []byte(`-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAOuxwdHldmUiYAeMjMQdRw6EwcFkas6HYTFUwRcHujqI=
-----END PUBLIC KEY-----
`)

// updateWindowCSS restyles Wails' update window as Kubereach; updateIcon is the app icon it shows.
//
//go:embed update-window.css
var updateWindowCSS string

//go:embed update-icon.png
var updateIcon []byte

// updaterConfig describes releases to Wails' updater through the update.json that each release publishes;
// GitHub serves latest/download from the newest release that is not a prerelease.
func updaterConfig(version string, key []byte) (updater.Config, error) {
	p, err := endpoint.New(endpoint.Config{URL: repoURL + "/releases/latest/download/update.json"})
	if err != nil {
		return updater.Config{}, err
	}
	window := &updater.BuiltinWindow{CSS: updateWindowCSS +
		".u .u__icon{background-image:url(data:image/png;base64," + base64.StdEncoding.EncodeToString(updateIcon) + ")}"}
	return updater.Config{CurrentVersion: version, Providers: []updater.Provider{p}, PublicKey: key, Window: window}, nil
}

// ServiceStartup checks for updates at launch and once a day while the app runs.
// Wails' CheckInterval would open its window on every tick, even to say there is nothing new.
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

// checkQuietly opens the update window only when a newer release is out, and not while a flow is already running.
func (a *AppService) checkQuietly(ctx context.Context) {
	if cfg, err := a.svc.LoadConfig(); err != nil || cfg.SkipUpdateCheck {
		return
	}
	switch a.app.Updater.State() {
	case updater.StateChecking, updater.StateDownloading, updater.StateVerifying, updater.StateInstalling:
		return
	}
	rel, err := a.app.Updater.Check(ctx)
	if err != nil {
		log.Print("update check: ", err)
		return
	}
	if rel != nil {
		a.CheckForUpdates()
	}
}

// retryFailedChecks makes Try Again check again after a check that failed. Wails' button asks for the download
// again, which does nothing when no release was found to download.
func (a *AppService) retryFailedChecks() {
	var checkFailed atomic.Bool
	a.app.Event.On(updater.EventCheckStarted, func(*application.CustomEvent) { checkFailed.Store(false) })
	a.app.Event.On(updater.EventError, func(e *application.CustomEvent) {
		info, _ := e.Data.(updater.ErrorInfo)
		checkFailed.Store(info.Stage == updater.StageCheck)
	})
	a.app.Event.On(updater.EventUserInstall, func(*application.CustomEvent) {
		if checkFailed.Swap(false) {
			a.CheckForUpdates()
		}
	})
}

// CheckForUpdates opens Wails' update window, which reports what it finds and installs it on the user's say.
func (a *AppService) CheckForUpdates() {
	go func() {
		if err := a.app.Updater.CheckAndInstall(context.Background()); err != nil {
			log.Print("update: ", err)
		}
	}()
}
