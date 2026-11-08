//go:build darwin

package tray

/*
void trayDispatchStart(void);
*/
import "C"

import (
	"sync"

	"fyne.io/systray"
)

var (
	startMu sync.Mutex
	pending func() // the systray start function waiting for the main thread
)

// run registers the tray in external-loop mode: Wails owns NSApplication and
// its run loop, the tray only adds a status item to it. Creating the status
// item has to happen on the main thread, but Wails calls OnStartup on a
// goroutine, so the start function is handed to the main dispatch queue.
//
// The returned end function does nothing: the library's own teardown stops
// NSApplication's run loop, which belongs to Wails, and the status item
// disappears when the process exits anyway.
func run(onReady, onExit func()) (end func()) {
	start, _ := systray.RunWithExternalLoop(onReady, onExit)
	startMu.Lock()
	pending = start
	startMu.Unlock()
	C.trayDispatchStart()
	return func() {}
}

// trayStartOnMain is called by the main dispatch queue (see tray_darwin.c).
//
//export trayStartOnMain
func trayStartOnMain() {
	startMu.Lock()
	start := pending
	pending = nil
	startMu.Unlock()
	if start != nil {
		start()
	}
}

// setIcons shows the icon of the state: the single color template image when
// there is one (the menu bar tints it for light and dark appearance), else
// the regular PNG.
func setIcons(o Options, alert bool) {
	regular := o.icon(alert)
	if tpl := o.template(alert); len(tpl) > 0 {
		systray.SetTemplateIcon(tpl, regular)
		return
	}
	if len(regular) > 0 {
		systray.SetIcon(regular)
	}
}
