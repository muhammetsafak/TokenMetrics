// Package tray puts an icon with a small menu (open, refresh now, quit) in
// the system notification area, so the application keeps running when its
// window is closed.
//
// It is built on fyne.io/systray, which works next to the Wails event loop on
// all three platforms:
//
//   - Windows: the library runs its own hidden window and message loop on a
//     dedicated OS thread; Wails' loop is untouched.
//   - Linux: StatusNotifierItem over D-Bus (pure Go). It does not touch GTK,
//     which the Wails webview owns. Desktops without a StatusNotifier host
//     (stock GNOME) are detected by Supported, and the application then
//     quits when its window is closed instead of hiding it.
//   - macOS: the library must not take over NSApplication, since Wails runs
//     it; it is started in "external loop" mode, on the main thread (see
//     tray_darwin.go).
package tray

import (
	"runtime"
	"sync"

	"fyne.io/systray"
)

// Options configures the tray icon. The callbacks run on their own
// goroutines and may block.
type Options struct {
	Tooltip string
	// IconICO is used on Windows (an .ico file), IconPNG on Linux (and on
	// macOS when there is no template icon).
	IconICO []byte
	IconPNG []byte
	// IconAlertICO and IconAlertPNG are the warning variants shown while a
	// budget is over its threshold (SetAlert). Empty: the normal icon stays.
	IconAlertICO []byte
	IconAlertPNG []byte
	// TemplatePNG and TemplateAlertPNG are the single color "template" images
	// of macOS, which the menu bar tints for light and dark appearance. Used
	// on macOS only; empty: IconPNG / IconAlertPNG are used instead.
	TemplatePNG      []byte
	TemplateAlertPNG []byte

	OpenLabel    string
	RefreshLabel string
	QuitLabel    string

	// OnOpen shows the window. It also runs on a left click of the icon on
	// Windows (elsewhere the click opens the menu).
	OnOpen func()
	// OnRefresh starts a scan now.
	OnRefresh func()
	// OnQuit ends the application.
	OnQuit func()
}

// icon returns the Windows / Linux icon (and the macOS fallback) for the
// state; a missing warning icon falls back to the normal one.
func (o Options) icon(alert bool) []byte {
	normal, warn := o.IconPNG, o.IconAlertPNG
	if runtime.GOOS == "windows" {
		normal, warn = o.IconICO, o.IconAlertICO
	}
	if alert && len(warn) > 0 {
		return warn
	}
	return normal
}

// template returns the macOS template image for the state, nil when there is
// none.
func (o Options) template(alert bool) []byte {
	if alert && len(o.TemplateAlertPNG) > 0 {
		return o.TemplateAlertPNG
	}
	return o.TemplatePNG
}

// iconSwitch remembers which icon variant is wanted and which one is on the
// screen, so the same value is never set twice and a value that arrives before
// the tray is ready is applied once it is. It only keeps state; the caller
// sets the icon outside its lock (the systray call may wait for the main
// thread).
type iconSwitch struct {
	mu         sync.Mutex
	want       bool
	applied    bool
	hasApplied bool
	active     bool
}

func (s *iconSwitch) set(alert bool) {
	s.mu.Lock()
	s.want = alert
	s.mu.Unlock()
}

// activate marks the tray as ready to take icon changes.
func (s *iconSwitch) activate() {
	s.mu.Lock()
	s.active = true
	s.hasApplied = false // a new status item shows nothing yet
	s.mu.Unlock()
}

// deactivate marks the tray as gone; nothing is due after it.
func (s *iconSwitch) deactivate() {
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}

// due returns the variant to put on the screen and true when it differs from
// what is shown (or nothing was shown yet), and records it as shown.
func (s *iconSwitch) due() (alert, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || (s.hasApplied && s.applied == s.want) {
		return false, false
	}
	s.applied, s.hasApplied = s.want, true
	return s.want, true
}

var (
	sw     iconSwitch
	swWake = make(chan struct{}, 1)
)

// SetAlert switches the tray icon to its warning variant (true) or back to
// the normal one. The same value again does nothing. A value set before Start
// is applied when the tray is ready; after the tray was stopped, the call has
// no effect. It never blocks.
func SetAlert(alert bool) {
	sw.set(alert)
	wakeSwitch()
}

func wakeSwitch() {
	select {
	case swWake <- struct{}{}:
	default:
	}
}

// Start shows the tray icon and returns the function that removes it. It does
// not block. Start is called once, from the application's OnStartup.
func Start(o Options) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() {
		once.Do(func() {
			sw.deactivate()
			close(done)
		})
	}

	onReady := func() {
		systray.SetTooltip(o.Tooltip)

		open := systray.AddMenuItem(o.OpenLabel, "")
		refresh := systray.AddMenuItem(o.RefreshLabel, "")
		systray.AddSeparator()
		quit := systray.AddMenuItem(o.QuitLabel, "")
		if runtime.GOOS == "windows" {
			systray.SetOnTapped(call(o.OnOpen))
		}

		// The icon is set from its own goroutine and never under a lock: on
		// macOS the call waits for the main thread, which is busy running
		// this function.
		sw.activate()
		go func() {
			for {
				select {
				case <-done:
					return
				case <-swWake:
					if alert, ok := sw.due(); ok {
						setIcons(o, alert)
					}
				}
			}
		}()
		wakeSwitch() // sets the first icon

		go func() {
			for {
				select {
				case <-done:
					return
				case <-open.ClickedCh:
					call(o.OnOpen)()
				case <-refresh.ClickedCh:
					call(o.OnRefresh)()
				case <-quit.ClickedCh:
					call(o.OnQuit)()
				}
			}
		}()
	}

	end := run(onReady, closeDone)
	return func() {
		closeDone()
		end()
	}
}

// call returns f as a goroutine launcher, so a slow callback cannot block the
// tray's event loop. A nil f is a no-op.
func call(f func()) func() {
	return func() {
		if f != nil {
			go f()
		}
	}
}
