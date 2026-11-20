package mineskin

import (
	"context"
	"errors"
)

// Close releases the client's resources and always returns nil. It is safe to
// call more than once and from several goroutines; a concurrent or second call
// blocks until the first one has finished, so every Close returns only once the
// client is fully shut down.
//
// Calls queued in a throttle, waits in WaitForJob and requests in flight fail
// with an error matching ErrClientClosed, and every call made
// afterwards fails the same way without sending a request. Close returns
// after the job poller and any grants load or refresh have exited.
//
// Close closes the idle connections only of the transport the client created
// itself; a transport given to WithTransport is left alone.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.cancelBase(ErrClientClosed)
		if tn := c.genGroup.Tuner; tn != nil {
			tn.Shutdown()
		}
		c.genGroup.Throttle.Close()
		c.getGroup.Throttle.Close()
		c.poller.Close(ErrClientClosed)
		c.bg.Wait()
		if c.idleCloser != nil {
			c.idleCloser()
		}
	})
	return nil
}

// checkOpen returns ErrClientClosed once Close has been called.
func (c *Client) checkOpen() error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	return nil
}

// requestContext returns a context that ends when ctx ends or the client is
// closed, in which case its cause is ErrClientClosed. The returned cancel
// func must be called to release the link to the base context.
func (c *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	merged, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(c.baseCtx, func() { cancel(context.Cause(c.baseCtx)) })
	return merged, func() {
		stop()
		cancel(context.Canceled)
	}
}

// closedOrSame returns err, or an ErrClientClosed error when err happened
// because Close cancelled merged while the caller's own ctx is still live.
func closedOrSame(ctx, merged context.Context, label string, err error) error {
	if ctx.Err() == nil && errors.Is(context.Cause(merged), ErrClientClosed) {
		return wrapClientError(label, ErrClientClosed)
	}
	return err
}
