package mineskin

import (
	"context"
)

// Close releases the client's resources and always returns nil. It is safe to
// call more than once and from several goroutines.
//
// Calls queued in a throttle, waits in WaitForJob and requests in flight fail
// with an error matching ErrClientClosed and ErrClient, and every call made
// afterwards fails the same way without sending a request. Close returns
// after the job poller and any grants load or refresh have exited. It closes idle connections only of the
// transport the client created itself; a transport given to WithTransport is
// left alone.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.cancelBase(ErrClientClosed)
		if tn := c.genGroup.tuner; tn != nil {
			tn.shutdown()
		}
		c.genGroup.throttle.Close()
		c.getGroup.throttle.Close()
		c.poller.close(ErrClientClosed)
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
