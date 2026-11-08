// Package wailsnotify delivers notify.Notifier messages through the Wails
// runtime (Windows toast, macOS notification center, Linux D-Bus).
package wailsnotify

import (
	"context"
	"fmt"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/muhammetsafak/TokenMetrics/internal/notify"
)

// Notifier sends notifications through the Wails runtime.
type Notifier struct {
	appCtx context.Context // the context Wails passed to OnStartup

	once    sync.Once
	ready   bool
	initErr error
}

// New returns a notifier bound to the Wails application context (the context
// given to the OnStartup callback).
func New(appCtx context.Context) *Notifier { return &Notifier{appCtx: appCtx} }

var _ notify.Notifier = (*Notifier)(nil)

func (n *Notifier) init() error {
	n.once.Do(func() {
		if err := runtime.InitializeNotifications(n.appCtx); err != nil {
			n.initErr = fmt.Errorf("notify: initialize: %w", err)
			return
		}
		if !runtime.IsNotificationAvailable(n.appCtx) {
			n.initErr = notify.ErrUnavailable
			return
		}
		// macOS asks the user once; the other platforms always say yes.
		ok, err := runtime.RequestNotificationAuthorization(n.appCtx)
		if err != nil {
			n.initErr = fmt.Errorf("notify: authorization: %w", err)
			return
		}
		if !ok {
			n.initErr = fmt.Errorf("%w: permission denied", notify.ErrUnavailable)
			return
		}
		n.ready = true
	})
	return n.initErr
}

// Notify implements notify.Notifier. A failed initialization is final for
// this run of the application: every later message gets the same error.
func (n *Notifier) Notify(ctx context.Context, m notify.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := n.init(); err != nil {
		return err
	}
	return runtime.SendNotification(n.appCtx, runtime.NotificationOptions{
		ID:    m.ID,
		Title: m.Title,
		Body:  m.Body,
	})
}

// Close releases the notification resources (the D-Bus connection on Linux).
// It must not run concurrently with Notify.
func (n *Notifier) Close() {
	if n.ready {
		runtime.CleanupNotifications(n.appCtx)
	}
}
