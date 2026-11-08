//go:build !linux

package tray

// Supported reports whether the platform can show a tray icon. Windows and
// macOS always can.
func Supported() bool { return true }
