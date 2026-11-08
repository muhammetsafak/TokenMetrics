//go:build !darwin

package tray

import (
	"runtime"

	"fyne.io/systray"
)

// run starts the tray on its own OS-thread-locked goroutine: the Windows
// implementation creates its window and pumps its messages on the calling
// thread, the Linux one just needs a goroutine to block in.
func run(onReady, onExit func()) (end func()) {
	go func() {
		runtime.LockOSThread()
		systray.Run(onReady, onExit)
	}()
	return systray.Quit
}

// setIcons shows the icon of the state. Template images are a macOS concept;
// here the regular icon (.ico on Windows, .png on Linux) is used.
func setIcons(o Options, alert bool) {
	if icon := o.icon(alert); len(icon) > 0 {
		systray.SetIcon(icon)
	}
}
