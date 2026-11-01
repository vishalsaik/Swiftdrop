package handlers

import "context"

// contextCancelled returns a context that is already cancelled, like the context
// of a request whose client has hung up.
func contextCancelled() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, cancel
}
