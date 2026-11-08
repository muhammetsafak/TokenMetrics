//go:build linux

package tray

import "github.com/godbus/dbus/v5"

// watcherName is the D-Bus name of the StatusNotifier host (KDE, and the
// AppIndicator extension on GNOME). Without an owner nobody draws the icon.
const watcherName = "org.kde.StatusNotifierWatcher"

// Supported reports whether a StatusNotifier host is running on the session
// bus. The answer decides whether closing the window may hide it: with no
// host there would be no way to bring it back.
func Supported() bool {
	conn, err := dbus.SessionBus() // shared connection; never closed here
	if err != nil {
		return false
	}
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, watcherName).Store(&has); err != nil {
		return false
	}
	return has
}
