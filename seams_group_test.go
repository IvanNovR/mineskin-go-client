package mineskin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
)

const (
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
	case <-time.After(waitTimeout(t)):
		t.Fatalf("%s did not finish", name)
		return nil
	}
}

// mustReceive waits for ch to be closed or to deliver a value.
func mustReceive(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitTimeout(t)):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func queueCall(c *Client) func() error {
	return func() error {
		_, err := c.Queue(context.Background(), FromURL("https://example.com/s.png"), GenerateOptions{})
		return err
	}
}

func generateCall(c *Client) func() error {
	return func() error {
		_, err := c.Generate(context.Background(), FromURL("https://example.com/s.png"), GenerateOptions{})
		return err
	}
}

// getCalls are one call per get-group endpoint.
func getCalls(c *Client) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		keySkin: func() error { _, err := c.GetSkin(ctx, testSkinUUID); return err },
		keyUser: func() error { _, err := c.GetUser(ctx); return err },
		keyJob:  func() error { _, err := c.GetJob(ctx, "job-1"); return err },
		keyList: func() error { _, err := c.ListJobs(ctx); return err },
	}
}

func oneSlot() Throttle { return Throttle{Interval: time.Nanosecond, Concurrency: 1} }

func TestGroupMappingGenerateHeldDoesNotBlockGets(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	// Granted is not yet inside the handler: wait until both really are.
	eventually(t, "two requests inside the handler", func() bool {
		n := peak()
		return n >= 2
	})
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
	t.Parallel()
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
	t.Parallel()
	srv := newHitServer(t, keySkin)
	c := newTestClient(t, srv.URL, WithGetThrottle(oneSlot()))
	first := goCall(getCalls(c)[keySkin])
	waitCount(t, srv, keySkin, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := goCall(func() error { _, err := c.GetSkin(ctx, testSkinUUID); return err })
	waitWaiting(t, c.getGroup, 1)
	other := goCall(getCalls(c)[keyUser])
	waitWaiting(t, c.getGroup, 2)

	cancel()
	err := mustDone(t, "cancelled call", cancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
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
	t.Parallel()
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
	t.Parallel()
	boom := errors.New("boom")
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, boom })
	c := newTestClient(t, "http://127.0.0.1:1", WithTransport(rt), WithGetThrottle(oneSlot()))
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout(t))
	defer cancel()
	for i := range 3 {
		if _, err := c.GetSkin(ctx, testSkinUUID); !errors.Is(err, boom) {
			t.Fatalf("call %d error = %v, want the transport error", i, err)
		}
	}
	if got := c.getGroup.Throttle.Stats().InFlight; got != 0 {
		t.Errorf("in flight = %d, want 0", got)
	}
}

func TestGroupNames(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, "http://127.0.0.1:1")
	if c.genGroup.Name != "generate" || c.getGroup.Name != "get" {
		t.Errorf("names = %q, %q", c.genGroup.Name, c.getGroup.Name)
	}
}

const (
	delayTick  = time.Millisecond
	plainQueue = `{"success":true,"job":{"id":"job-1","status":"waiting"}}`
)

// reply is a canned response of replyServer.
type reply struct {
	status     int
	retryAfter string
	body       string
}

// replyServer answers each "METHOD /path" key with its canned reply (200 with
// a minimal body for unknown keys) and may hold keys on a gate.
func replyServer(t *testing.T, replies map[string]reply, gates ...string) *clientServer {
	t.Helper()
	s := &clientServer{hits: map[string]int{}, gates: map[string]chan struct{}{}}
	for _, k := range gates {
		s.gates[k] = make(chan struct{})
	}
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
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
		rep, ok := replies[key]
		if !ok {
			rep = reply{status: 200, body: skinMinimal}
		}
		if rep.retryAfter != "" {
			w.Header().Set("Retry-After", rep.retryAfter)
		}
		writeJSON(rep.status, rep.body)(w, r)
	})
	s.URL = srv.URL
	return s
}

// delayClient builds a client with a fake clock and wide-open groups, so only
// the intervals of 1 ns and server delays can hold a request.
func delayClient(t *testing.T, srv *clientServer, opts ...Option) (*Client, *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(pollerT0)
	return newTestClient(t, srv.URL, append([]Option{withClock(fake)}, opts...)...), fake
}

// assertDelayed checks that call, started now on g, stays queued until the
// fake clock has advanced by exactly want (any want <= 0 means no delay).
func assertDelayed(t *testing.T, fake *clock.Fake, g *ratelimit.Group, call func() error, want time.Duration) error {
	t.Helper()
	res := goCall(call)
	if want <= 0 {
		fake.Advance(delayTick)
		return mustDone(t, "undelayed call", res)
	}
	waitWaiting(t, g, 1)
	fake.Advance(delayTick)
	fake.Advance(want - 2*delayTick)
	if got := g.Throttle.Stats().Waiting; got != 1 {
		t.Fatalf("call left the queue before the %v delay elapsed", want)
	}
	fake.Advance(delayTick)
	return mustDone(t, "delayed call", res)
}

func nextRelative(ms int, success bool) string {
	rl := `"rateLimit":{"next":{"relative":` + strconv.Itoa(ms) + `}}`
	if success {
		return `{"success":true,"job":{"id":"job-1","status":"waiting"},` + rl + `}`
	}
	return `{"success":false,"errors":[{"code":"rate_limited","message":"slow down"}],` + rl + `}`
}

func TestServerDelayFromBodySuccess(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keyQueue: {200, "", nextRelative(5000, true)}})
	c, fake := delayClient(t, srv)
	if err := queueCall(c)(); err != nil {
		t.Fatalf("first Queue error = %v", err)
	}
	if err := getCalls(c)[keySkin](); err != nil {
		t.Fatalf("GetSkin must not be held by the generate delay: %v", err)
	}
	if err := assertDelayed(t, fake, c.genGroup, queueCall(c), 5*time.Second); err != nil {
		t.Errorf("second Queue error = %v", err)
	}
}

func TestServerDelayFromBodyOnAPIError(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keyQueue: {429, "", nextRelative(3000, false)}})
	// Retries off: this tests the delay of a single response; retries are in retry_test.go.
	c, fake := delayClient(t, srv, WithRateLimitRetries(0))
	err := queueCall(c)()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "rate_limited" || apiErr.Message != "slow down" {
		t.Fatalf("error = %v, want the original *APIError", err)
	}
	err = assertDelayed(t, fake, c.genGroup, queueCall(c), 3*time.Second)
	if !errors.As(err, &apiErr) {
		t.Errorf("second error = %v, want *APIError", err)
	}
}

func TestServerDelayOnGetGroup(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keySkin: {200, "", `{"success":true,"skin":{},"rateLimit":{"next":{"relative":4000}}}`}})
	c, fake := delayClient(t, srv)
	calls := getCalls(c)
	if err := calls[keySkin](); err != nil {
		t.Fatalf("GetSkin error = %v", err)
	}
	if err := queueCall(c)(); err != nil {
		t.Fatalf("Queue must not be delayed by a get response: %v", err)
	}
	if err := assertDelayed(t, fake, c.getGroup, calls[keyJob], 4*time.Second); err != nil {
		t.Errorf("GetJob error = %v", err)
	}
}

func TestRetryAfterOnRetryableJSONAndNonJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rep  reply
		want time.Duration
	}{
		{"503 html with Retry-After", reply{503, "4", "<html>down</html>"}, 4 * time.Second},
		{"429 json takes the later of the two", reply{429, "2", nextRelative(3000, false)}, 3 * time.Second},
		{"503 json takes the later Retry-After", reply{503, "8", nextRelative(3000, false)}, 8 * time.Second},
		{"409 is not retryable", reply{409, "9", `{"success":false,"errors":[{"code":"conflict","message":"x"}]}`}, 0},
		{"200 ignores Retry-After", reply{200, "5", plainQueue}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := replyServer(t, map[string]reply{keyQueue: tt.rep})
			// Retries off: this tests the delay of a single response; retries are in retry_test.go.
			c, fake := delayClient(t, srv, WithRateLimitRetries(0))
			_ = queueCall(c)()
			_ = assertDelayed(t, fake, c.genGroup, queueCall(c), tt.want)
		})
	}
}

func TestServerDelayMissingOrInvalidIsNoDelay(t *testing.T) {
	t.Parallel()
	const head = `{"success":true,"job":{"id":"job-1","status":"waiting"},`
	tests := []struct {
		name    string
		rep     reply
		wantErr bool
	}{
		{"no rateLimit", reply{200, "", plainQueue}, false},
		{"next null", reply{200, "", head + `"rateLimit":{"next":null}}`}, false},
		{"negative relative", reply{200, "", head + `"rateLimit":{"next":{"relative":-5000}}}`}, false},
		{"string relative", reply{200, "", head + `"rateLimit":{"next":{"relative":"5000"}}}`}, false},
		{"fraction relative", reply{200, "", head + `"rateLimit":{"next":{"relative":1.5}}}`}, false},
		{"garbage Retry-After", reply{503, "soon", "<html>down</html>"}, true},
		{"garbage Retry-After on json error", reply{429, "soon", `{"success":false,"errors":[{"code":"c","message":"m"}]}`}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := replyServer(t, map[string]reply{keyQueue: tt.rep})
			// Retries off: this tests the delay of a single response; retries are in retry_test.go.
			c, fake := delayClient(t, srv, WithRateLimitRetries(0))
			if err := queueCall(c)(); (err != nil) != tt.wantErr {
				t.Fatalf("first error = %v, wantErr %v", err, tt.wantErr)
			}
			_ = assertDelayed(t, fake, c.genGroup, queueCall(c), 0)
		})
	}
}

func TestDeadlineAppliedBeforeRelease(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keyQueue: {200, "", nextRelative(5000, true)}}, keyQueue)
	c, fake := delayClient(t, srv, WithGenerateThrottle(oneSlot()))
	first := goCall(queueCall(c))
	waitCount(t, srv, keyQueue, 1)
	second := goCall(queueCall(c))
	waitWaiting(t, c.genGroup, 1)
	fake.Advance(delayTick) // the interval is over, only the slot holds the waiter back

	srv.open(keyQueue)
	if err := mustDone(t, "first", first); err != nil {
		t.Fatalf("first error = %v", err)
	}
	st := c.genGroup.Throttle.Stats()
	if st.Waiting != 1 || st.InFlight != 0 {
		t.Fatalf("after release: waiting=%d inFlight=%d, want 1 and 0 (the delay must be set before release)", st.Waiting, st.InFlight)
	}
	fake.Advance(5 * time.Second)
	if err := mustDone(t, "second", second); err != nil {
		t.Errorf("second error = %v", err)
	}
}

func TestServerDelayIsCappedAtOneHourButAPIErrorKeepsRawRetryAfter(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keyQueue: {429, "99999999", nextRelative(3000, false)}})
	c, fake := delayClient(t, srv, WithRateLimitRetries(0))
	var apiErr *APIError
	if err := queueCall(c)(); !errors.As(err, &apiErr) || apiErr.RetryAfter != 99999999*time.Second {
		t.Fatalf("error = %v, want *APIError with the raw RetryAfter of 99999999s", err)
	}
	if err := assertDelayed(t, fake, c.genGroup, queueCall(c), time.Hour); !errors.As(err, &apiErr) {
		t.Errorf("second error = %v, want *APIError once the capped hour elapsed", err)
	}
}

func TestServerDelayCapOnHugeNextRelative(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keyQueue: {200, "", `{"success":true,"job":{"id":"job-1","status":"waiting"},"rateLimit":{"next":{"relative":9223372036854}}}`}})
	c, fake := delayClient(t, srv)
	if err := queueCall(c)(); err != nil {
		t.Fatal(err)
	}
	if err := assertDelayed(t, fake, c.genGroup, queueCall(c), time.Hour); err != nil {
		t.Errorf("delayed Queue error = %v", err)
	}
}

func TestOverflowingRetryAfterClampsToOneHourAndAPIErrorKeepsZero(t *testing.T) {
	t.Parallel()
	srv := replyServer(t, map[string]reply{keyQueue: {429, "99999999999999999999", nextRelative(1000, false)}})
	c, fake := delayClient(t, srv, WithRateLimitRetries(0))
	var apiErr *APIError
	if err := queueCall(c)(); !errors.As(err, &apiErr) || apiErr.RetryAfter != 0 {
		t.Fatalf("error = %v, want *APIError with RetryAfter 0 (an overflowing header is not parsed)", err)
	}
	// The body asks for 1 s, but the overflowing header wins and is capped at 1 h.
	if err := assertDelayed(t, fake, c.genGroup, queueCall(c), time.Hour); !errors.As(err, &apiErr) {
		t.Errorf("second error = %v, want *APIError", err)
	}
}
