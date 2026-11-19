package mineskin

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
)

const (
	waitTimeout = 5 * time.Second
	waitPoll    = time.Millisecond

	keyQueue    = "POST /v2/queue"
	keyGenerate = "POST /v2/generate"
	keySkin     = "GET /v2/skins/" + testSkinID
	keyUser     = "GET /v2/me"
	keyJob      = "GET /v2/queue/job-1"
	keyList     = "GET /v2/queue"

	jobFixture  = `{"success":true,"job":{"id":"job-1","status":"waiting"}}`
	listFixture = `{"success":true,"jobs":[]}`
	meFixture   = `{"user":"744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11","grants":{}}`
	skinMinimal = `{"success":true,"skin":{"uuid":"744c993e0e8a4f0b9b6a2f3c7a1b5d11"}}`
)

// clientServer is a test API that counts requests per "METHOD /path" key and can
// hold requests of chosen keys until the test opens their gate.
type clientServer struct {
	URL   string
	mu    sync.Mutex
	hits  map[string]int
	gates map[string]chan struct{}
}

// newHitServer starts the server. Keys in gated hold their requests until
// open is called for the key.
func newHitServer(t *testing.T, gated ...string) *clientServer {
	t.Helper()
	s := &clientServer{hits: map[string]int{}, gates: map[string]chan struct{}{}}
	for _, k := range gated {
		s.gates[k] = make(chan struct{})
	}
	srv := newTestServer(t, s.handle)
	s.URL = srv.URL
	return s
}

func (s *clientServer) handle(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.hits[key]++
	gate := s.gates[key]
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	body := skinMinimal
	switch key {
	case keyQueue, keyGenerate:
		body = queueFixture
	case keyUser:
		body = meFixture
	case keyJob:
		body = jobFixture
	case keyList:
		body = listFixture
	}
	writeJSON(200, body)(w, r)
}

// count returns the number of requests seen for key.
func (s *clientServer) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[key]
}

// open releases every request held on key, now and later.
func (s *clientServer) open(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.gates[key]; ok {
		close(g)
		delete(s.gates, key)
	}
}

// waitWaiting waits until n requests are queued in g's throttle.
func waitWaiting(t *testing.T, g *ratelimit.Group, n int) {
	t.Helper()
	eventually(t, "queued requests", func() bool { return g.Throttle.Stats().Waiting == n })
}

// waitCount waits until the server has seen n requests for key.
func waitCount(t *testing.T, s *clientServer, key string, n int) {
	t.Helper()
	eventually(t, key, func() bool { return s.count(key) >= n })
}

// goCall runs f in a goroutine and returns a channel with its error.
func goCall(f func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- f() }()
	return ch
}

func mustDone(t *testing.T, name string, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(waitTimeout):
		t.Fatalf("%s did not finish", name)
		return nil
	}
}

// mustReceive waits for ch to be closed or to deliver a value.
func mustReceive(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func queueCall(c *Client) func() error {
	return func() error {
		_, err := c.Queue(context.Background(), FromURL("https://example.com/s.png"), nil)
		return err
	}
}

func generateCall(c *Client) func() error {
	return func() error {
		_, err := c.Generate(context.Background(), FromURL("https://example.com/s.png"), nil)
		return err
	}
}

// getCalls are one call per get-group endpoint.
func getCalls(c *Client) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		keySkin: func() error { _, err := c.GetSkin(ctx, testSkinID); return err },
		keyUser: func() error { _, err := c.GetUser(ctx); return err },
		keyJob:  func() error { _, err := c.GetJob(ctx, "job-1"); return err },
		keyList: func() error { _, err := c.ListJobs(ctx); return err },
	}
}

func oneSlot() Throttle { return Throttle{Interval: time.Nanosecond, Concurrency: 1} }

func TestGroupMappingGenerateHeldDoesNotBlockGets(t *testing.T) {
	srv := newHitServer(t, keyQueue)
	c := newTestClient(t, srv.URL, WithGenerateThrottle(oneSlot()))
	held := goCall(queueCall(c))
	waitCount(t, srv, keyQueue, 1)
	second := goCall(generateCall(c))
	waitWaiting(t, c.genGroup, 1)

	for key, call := range getCalls(c) {
		if err := call(); err != nil {
			t.Errorf("%s error = %v", key, err)
		}
	}
	if got := srv.count(keyGenerate); got != 0 {
		t.Errorf("second Generate reached the server while the slot was held (%d hits)", got)
	}
	srv.open(keyQueue)
	for name, ch := range map[string]<-chan error{"Queue": held, "Generate": second} {
		if err := mustDone(t, name, ch); err != nil {
			t.Errorf("%s error = %v", name, err)
		}
	}
	if got := srv.count(keyGenerate); got != 1 {
		t.Errorf("Generate hits = %d, want 1", got)
	}
}

func TestGroupMappingGetHeldBlocksGetsButNotQueue(t *testing.T) {
	srv := newHitServer(t, keySkin)
	c := newTestClient(t, srv.URL, WithGetThrottle(oneSlot()))
	held := goCall(getCalls(c)[keySkin])
	waitCount(t, srv, keySkin, 1)
	if err := queueCall(c)(); err != nil {
		t.Fatalf("Queue error = %v", err)
	}
	calls := getCalls(c)
	chans := map[string]<-chan error{
		keyJob:  goCall(calls[keyJob]),
		keyList: goCall(calls[keyList]),
		keyUser: goCall(calls[keyUser]),
	}
	waitWaiting(t, c.getGroup, 3)
	for _, key := range []string{keyJob, keyList, keyUser} {
		if got := srv.count(key); got != 0 {
			t.Errorf("%s reached the server while the get slot was held", key)
		}
	}
	srv.open(keySkin)
	if err := mustDone(t, "GetSkin", held); err != nil {
		t.Errorf("GetSkin error = %v", err)
	}
	for key, ch := range chans {
		if err := mustDone(t, key, ch); err != nil {
			t.Errorf("%s error = %v", key, err)
		}
	}
}

func TestGetConcurrencyIsRealInFlight(t *testing.T) {
	release := make(chan struct{})
	h, peak := trackConcurrency(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(200, skinMinimal)(w, r)
	})
	srv := newTestServer(t, h)
	c := newTestClient(t, srv.URL, WithGetThrottle(Throttle{Interval: time.Nanosecond, Concurrency: 2}))

	var chans []<-chan error
	for range 5 {
		chans = append(chans, goCall(getCalls(c)[keySkin]))
	}
	waitWaiting(t, c.getGroup, 3)
	close(release)
	for i, ch := range chans {
		if err := mustDone(t, "GetSkin", ch); err != nil {
			t.Errorf("call %d error = %v", i, err)
		}
	}
	if got := peak(); got != 2 {
		t.Errorf("peak concurrency = %d, want 2", got)
	}
}

func TestGetIntervalWithFakeClock(t *testing.T) {
	srv := newHitServer(t)
	fake := clock.NewFake(pollerT0)
	c := newTestClient(t, srv.URL, withClock(fake),
		WithGetThrottle(Throttle{Interval: time.Second, Concurrency: 5}))

	var chans []<-chan error
	for range 3 {
		chans = append(chans, goCall(getCalls(c)[keySkin]))
	}
	waitCount(t, srv, keySkin, 1)
	waitWaiting(t, c.getGroup, 2)
	if got := srv.count(keySkin); got != 1 {
		t.Fatalf("hits before advance = %d, want 1", got)
	}
	fake.Advance(time.Second)
	waitCount(t, srv, keySkin, 2)
	waitWaiting(t, c.getGroup, 1)
	if got := srv.count(keySkin); got != 2 {
		t.Fatalf("hits after 1s = %d, want 2", got)
	}
	fake.Advance(time.Second)
	waitCount(t, srv, keySkin, 3)
	for i, ch := range chans {
		if err := mustDone(t, "GetSkin", ch); err != nil {
			t.Errorf("call %d error = %v", i, err)
		}
	}
}

func TestQueuedCallReturnsCtxErr(t *testing.T) {
	srv := newHitServer(t, keySkin)
	c := newTestClient(t, srv.URL, WithGetThrottle(oneSlot()))
	first := goCall(getCalls(c)[keySkin])
	waitCount(t, srv, keySkin, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := goCall(func() error { _, err := c.GetSkin(ctx, testSkinID); return err })
	waitWaiting(t, c.getGroup, 1)
	other := goCall(getCalls(c)[keyUser])
	waitWaiting(t, c.getGroup, 2)

	cancel()
	err := mustDone(t, "cancelled call", cancelled)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrClient) {
		t.Fatalf("error = %v, want context.Canceled and ErrClient", err)
	}
	waitWaiting(t, c.getGroup, 1)
	srv.open(keySkin)
	for name, ch := range map[string]<-chan error{"first": first, "other": other} {
		if err := mustDone(t, name, ch); err != nil {
			t.Errorf("%s error = %v", name, err)
		}
	}
	if got := srv.count(keySkin); got != 1 {
		t.Errorf("skin hits = %d, want 1 (cancelled call must not reach the server)", got)
	}
}

func TestTimeoutExcludesQueueTime(t *testing.T) {
	// The start interval keeps the second request queued for longer than the
	// timeout (a lower bound the throttle guarantees); the handler answers at
	// once, so the exchange itself is far inside the timeout.
	const (
		timeout  = 50 * time.Millisecond
		interval = 3 * timeout
	)
	srv := newTestServer(t, writeJSON(200, skinMinimal))
	c := newTestClient(t, srv.URL, WithTimeout(timeout), WithGetThrottle(Throttle{Interval: interval, Concurrency: 1}))
	call := getCalls(c)[keySkin]
	if err := call(); err != nil {
		t.Fatalf("first error = %v", err)
	}
	start := time.Now()
	if err := call(); err != nil {
		t.Errorf("second error = %v, want success (queue time must not count)", err)
	}
	if queued := time.Since(start); queued <= timeout {
		t.Errorf("second call took %v, want more than the %v timeout so the test proves something", queued, timeout)
	}
}

func TestReleaseOnTransportError(t *testing.T) {
	boom := errors.New("boom")
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, boom })
	c := newTestClient(t, "http://127.0.0.1:1", WithTransport(rt), WithGetThrottle(oneSlot()))
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	for i := range 3 {
		if _, err := c.GetSkin(ctx, testSkinID); !errors.Is(err, boom) {
			t.Fatalf("call %d error = %v, want the transport error", i, err)
		}
	}
	if got := c.getGroup.Throttle.Stats().InFlight; got != 0 {
		t.Errorf("in flight = %d, want 0", got)
	}
}

func TestExchangeNilContextIsInvalidInput(t *testing.T) {
	srv := newHitServer(t)
	c := newTestClient(t, srv.URL)
	var nilCtx context.Context
	_, err := c.GetUser(nilCtx)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if srv.count(keyUser) != 0 {
		t.Error("a request was sent for a nil context")
	}
}

func TestGroupNames(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	if c.genGroup.Name != "generate" || c.getGroup.Name != "get" {
		t.Errorf("names = %q, %q", c.genGroup.Name, c.getGroup.Name)
	}
}
