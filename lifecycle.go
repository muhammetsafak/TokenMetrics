package main

import (
	"context"
	_ "embed"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	appsvc "github.com/muhammetsafak/TokenMetrics/internal/app"
	"github.com/muhammetsafak/TokenMetrics/internal/notify/wailsnotify"
	"github.com/muhammetsafak/TokenMetrics/internal/tray"
)

// Tray icons (build/tray): an .ico for Windows, a .png for Linux.
var (
	//go:embed build/tray/tray.ico
	trayICO []byte
	//go:embed build/tray/tray.png
	trayPNG []byte
	// The warning variants (a budget is over its threshold) and the single
	// color template images of macOS.
	//go:embed build/tray/tray-alert.ico
	trayAlertICO []byte
	//go:embed build/tray/tray-alert.png
	trayAlertPNG []byte
	//go:embed build/tray/tray-template.png
	trayTemplatePNG []byte
	//go:embed build/tray/tray-template-alert.png
	trayTemplateAlertPNG []byte
)

// singleInstanceID names the single-instance lock: a second launch does not
// open a second window, it brings the first one forward.
const singleInstanceID = "tunedness.tokenmetrics.4c1f7a52-9b3e-4d0a-8e6c-5a2b7d91f3c8"

// lifecycle ties what keeps the application alive without a window (the
// background refresh, budget notifications and the tray icon) to the Wails
// lifecycle. App itself only serves the bindings.
type lifecycle struct {
	app *App
	// tray reports whether closing the window hides it (the tray icon is
	// there to bring it back); decided before the window exists.
	tray bool

	bg       *appsvc.Background
	notifier *wailsnotify.Notifier
	stopTray func()
}

func newLifecycle(app *App) *lifecycle {
	return &lifecycle{app: app, tray: tray.Supported()}
}

// startup runs the application's own startup (service, first scan), then
// starts the background work and the tray icon.
func (l *lifecycle) startup(ctx context.Context) {
	l.app.startup(ctx)

	if svc := l.app.svc; svc != nil {
		l.notifier = wailsnotify.New(l.app.ctx)
		var onAlert func(bool) // only with a tray: no one needs the state else
		if l.tray {
			onAlert = tray.SetAlert
		}
		l.bg = appsvc.NewBackground(svc, appsvc.BackgroundConfig{
			Notifier: l.notifier,
			// A background scan reports its end like a manual one, so the
			// dashboard reloads; it stays quiet while it runs.
			OnDone: func(sum appsvc.ScanSummaryDTO) {
				runtime.EventsEmit(l.app.ctx, eventScanDone, sum)
			},
			// The tray shows its warning icon while a budget is over its
			// threshold (independent of the notification setting).
			OnBudgetAlert: onAlert,
		})
		if err := l.bg.Start(l.app.ctx); err != nil {
			l.app.log.Error("start background refresh", "error", err)
		}
	}

	if l.tray {
		l.stopTray = tray.Start(tray.Options{
			Tooltip:          "TokenMetrics",
			IconICO:          trayICO,
			IconPNG:          trayPNG,
			IconAlertICO:     trayAlertICO,
			IconAlertPNG:     trayAlertPNG,
			TemplatePNG:      trayTemplatePNG,
			TemplateAlertPNG: trayTemplateAlertPNG,
			OpenLabel:        "Aç",
			RefreshLabel:     "Şimdi yenile",
			QuitLabel:        "Çık",
			OnOpen:           l.showWindow,
			OnRefresh:        l.refresh,
			OnQuit:           func() { runtime.Quit(l.app.ctx) },
		})
	}
}

// shutdown stops the tray and the background work before the application
// closes the database.
func (l *lifecycle) shutdown(ctx context.Context) {
	if l.stopTray != nil {
		l.stopTray()
	}
	if l.bg != nil {
		l.bg.Stop()
	}
	if l.notifier != nil {
		l.notifier.Close()
	}
	l.app.shutdown(ctx)
}

// showWindow brings the window back from the tray or from behind.
func (l *lifecycle) showWindow() {
	ctx := l.app.ctx
	if ctx == nil {
		return
	}
	if goruntime.GOOS == "darwin" {
		runtime.Show(ctx) // un-hides the application (close hides it as a whole)
	}
	runtime.WindowUnminimise(ctx)
	runtime.WindowShow(ctx)
}

// refresh scans every active source now ("Şimdi yenile"). A scan that is
// already running is left alone.
func (l *lifecycle) refresh() {
	if l.app.svc == nil {
		return
	}
	l.app.startScan()
}

// onSecondLaunch is called in the running instance when the application is
// started a second time.
func (l *lifecycle) onSecondLaunch(options.SecondInstanceData) {
	go l.showWindow() // keep the platform's message loop free
}
