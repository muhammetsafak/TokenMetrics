// Package notify defines how the application shows desktop notifications. The
// rest of the code only knows the Notifier interface, so budget logic is
// tested with a fake. The real delivery (Windows toast, macOS notification
// center, Linux D-Bus notification) is in the wailsnotify subpackage, which
// uses the notifier that ships with the Wails runtime; no extra dependency
// is needed.
package notify

import (
	"context"
	"errors"
)

// ErrUnavailable is returned when the platform cannot show notifications (no
// notification service on Linux, permission denied on macOS).
var ErrUnavailable = errors.New("notify: desktop notifications are not available")

// Message is one notification.
type Message struct {
	// ID identifies the notification; sending the same ID again replaces the
	// earlier one on platforms that support it.
	ID    string
	Title string
	Body  string
}

// Notifier shows a notification to the user.
type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// Func adapts a function to a Notifier.
type Func func(ctx context.Context, m Message) error

// Notify implements Notifier.
func (f Func) Notify(ctx context.Context, m Message) error { return f(ctx, m) }
