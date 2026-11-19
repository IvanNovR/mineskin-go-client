package mineskin

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestStartGateDeclinedRequestSendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	c := newTestClient(t, srv.URL)

	_, err := c.getJobGated(context.Background(), "j1", func() bool { return false })

	if !errors.Is(err, errCheckSkipped) {
		t.Errorf("err = %v, want errCheckSkipped", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server hits = %d, want 0", n)
	}
}
