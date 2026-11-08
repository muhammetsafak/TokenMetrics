//go:build darwin

#include <dispatch/dispatch.h>
#include "_cgo_export.h"

static void trayRunOnMain(void *unused) {
	trayStartOnMain();
}

// trayDispatchStart queues trayStartOnMain on the main thread. It returns at
// once; the block runs when the main run loop gets to it.
void trayDispatchStart(void) {
	dispatch_async_f(dispatch_get_main_queue(), NULL, trayRunOnMain);
}
