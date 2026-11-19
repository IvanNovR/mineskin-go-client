//go:build live

package mineskin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// defaultLiveSkinID is the first fixed skin id of the Java client's GetTest
	// (tests/src/test/java/test/GetTest.java at 744c993).
	defaultLiveSkinID = "c1a1982831874868a37f5be375d38d5b"

	liveTimeout         = 30 * time.Second
	liveGenTimeout      = 2 * time.Minute
	liveMinTimestamp    = "2014-01-01T00:00:00Z"
	liveMinDuration     = 100 * time.Millisecond
	liveMaxDuration     = 10 * time.Minute
	liveMaxFutureSkew   = 24 * time.Hour
	livePaidMinConcur   = 5
	livePaidMinPerMinut = 50
)

// liveClient returns a client for the real API, skipping the test when
// MINESKIN_API_KEY is not set. The key is never logged.
func liveClient(t *testing.T) *Client {
	t.Helper()
	key := os.Getenv("MINESKIN_API_KEY")
	if strings.TrimSpace(key) == "" {
		t.Skip("MINESKIN_API_KEY not set")
	}
	c, err := NewClient(WithAPIKey(key), WithUserAgent("mineskin-go-client-tests"))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

var (
	genClientOnce sync.Once
	genClient     *Client
	genClientErr  error
)

// TestMain closes the shared generate client after all live tests ran.
func TestMain(m *testing.M) {
	code := m.Run()
	if genClient != nil {
		_ = genClient.Close()
	}
	os.Exit(code)
}

// sharedGenClient returns the one client every generate-path live test uses,
// so that its generate throttle and the server delays carry over between
// tests. It is closed by TestMain, not by a single test. It skips the test
// when MINESKIN_API_KEY is not set. The key is never logged.
func sharedGenClient(t *testing.T) *Client {
	t.Helper()
	key := os.Getenv("MINESKIN_API_KEY")
	if strings.TrimSpace(key) == "" {
		t.Skip("MINESKIN_API_KEY not set")
	}
	genClientOnce.Do(func() {
		genClient, genClientErr = NewClient(WithAPIKey(key), WithUserAgent("mineskin-go-client-tests"),
			WithTimeout(liveGenHTTPLimit),
			WithJobPolling(PollOptions{MaxAttempts: liveMaxPollAttempts, UseETA: true}))
	})
	if genClientErr != nil {
		t.Fatalf("NewClient() error = %v", genClientErr)
	}
	return genClient
}

func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	t.Cleanup(cancel)
	return ctx
}

func TestLiveGetSkin(t *testing.T) {
	c := liveClient(t)
	id := os.Getenv("MINESKIN_TEST_SKIN_UUID")
	isDefault := id == ""
	if isDefault {
		id = defaultLiveSkinID
	}

	res, err := c.GetSkin(liveContext(t), id)
	if err != nil {
		t.Fatalf("GetSkin() error = %v", err)
	}
	if !res.Success || res.Status != http.StatusOK {
		t.Fatalf("Success = %v, Status = %d; want true, 200", res.Success, res.Status)
	}
	want, err := ParseUUID(id)
	if err != nil {
		t.Fatalf("ParseUUID(%q) error = %v", id, err)
	}
	if res.Skin.UUID != want {
		t.Errorf("Skin.UUID = %s, want %s", res.Skin.UUID, want)
	}
	if res.Skin.Texture.Data.Value == "" || res.Skin.Texture.Data.Signature == "" {
		t.Error("texture value or signature is empty")
	}
	if isDefault && res.Skin.Visibility != VisibilityUnlisted {
		t.Errorf("Visibility = %q, want %q", res.Skin.Visibility, VisibilityUnlisted)
	}

	g := res.Skin.Generator
	t.Logf("generator: timestamp=%s (unix ms %d) duration=%s", g.Timestamp.UTC().Format(time.RFC3339), g.Timestamp.UnixMilli(), g.Duration)
	minTS, _ := time.Parse(time.RFC3339, liveMinTimestamp)
	if !g.Timestamp.IsZero() && (g.Timestamp.Before(minTS) || g.Timestamp.After(time.Now().Add(liveMaxFutureSkew))) {
		t.Errorf("generator timestamp %s is implausible for milliseconds", g.Timestamp.UTC())
	}
	if g.Duration != 0 && (g.Duration < liveMinDuration || g.Duration > liveMaxDuration) {
		t.Errorf("generator duration %s is implausible for milliseconds", g.Duration)
	}
}

func TestLiveGetSkinNotFound(t *testing.T) {
	c := liveClient(t)
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}

	_, err := c.GetSkinByUUID(liveContext(t), UUID(b))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSkinByUUID() error = %v, want ErrNotFound", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("error = %#v, want *APIError with status 404", err)
	}
}

func TestLiveGetUser(t *testing.T) {
	c := liveClient(t)

	res, err := c.GetUser(liveContext(t))
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			t.Fatalf("GetUser() failed: HTTP %d, code %q, message %q (the key may lack access to /v2/me; consider gating this test behind MINESKIN_LIVE_PAID): %v",
				apiErr.Status, apiErr.Code, apiErr.Message, err)
		}
		t.Fatalf("GetUser() failed: %v", err)
	}
	if !res.Success {
		t.Errorf("Success = false, want true")
	}
	if res.User.UUID == (UUID{}) {
		t.Error("User.UUID is zero")
	}
}

func TestLiveGetUserPaidGrants(t *testing.T) {
	if os.Getenv("MINESKIN_LIVE_PAID") != "1" {
		t.Skip("MINESKIN_LIVE_PAID=1 not set")
	}
	c := liveClient(t)

	res, err := c.GetUser(liveContext(t))
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}
	g := res.User.Grants
	if g.Concurrency == nil || *g.Concurrency <= livePaidMinConcur {
		t.Errorf("Grants.Concurrency = %v, want > %d", g.Concurrency, livePaidMinConcur)
	}
	if g.PerMinute == nil || *g.PerMinute <= livePaidMinPerMinut {
		t.Errorf("Grants.PerMinute = %v, want > %d", g.PerMinute, livePaidMinPerMinut)
	}
}

const (
	liveSkinSize     = 64
	liveNameBytes    = 4
	liveGenHTTPLimit = 60 * time.Second

	liveMaxPollAttempts = 30
	liveSmokeCalls      = 3
	liveSmokeTimeout    = 5 * time.Minute
)

// randomSkinImage returns a 64x64 image of random pixels.
func randomSkinImage() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, liveSkinSize, liveSkinSize))
	if _, err := rand.Read(img.Pix); err != nil {
		panic(fmt.Sprintf("rand.Read: %v", err))
	}
	return img
}

// randomSkinName returns a short random skin name.
func randomSkinName(t *testing.T) string {
	t.Helper()
	var b [liveNameBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return "go" + hex.EncodeToString(b[:])
}

func liveGenContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveGenTimeout)
	t.Cleanup(cancel)
	return ctx
}

func TestLiveQueueURLAndWait(t *testing.T) {
	c := sharedGenClient(t)
	ctx := liveGenContext(t)
	name := randomSkinName(t)

	q, err := c.Queue(ctx, FromURL(fmt.Sprintf("https://api.mineskin.org/random-image?t=%d", time.Now().Unix())),
		&GenerateOptions{Visibility: VisibilityUnlisted, Name: name})
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	t.Logf("queued job: id=%s status=%s timestamp=%s (unix ms %d) eta=%s (unix ms %d)",
		q.Job.ID, q.Job.Status, q.Job.Timestamp.UTC().Format(time.RFC3339), q.Job.Timestamp.UnixMilli(),
		q.Job.ETA.UTC().Format(time.RFC3339), q.Job.ETA.UnixMilli())
	if q.RateLimit != nil {
		t.Logf("rateLimit: delay=%s next=%+v limit=%+v", q.RateLimit.Delay, q.RateLimit.Next, q.RateLimit.Limit)
	}

	res, err := c.WaitForJob(ctx, q.Job)
	if err != nil {
		t.Fatalf("WaitForJob() error = %v", err)
	}
	skin, err := c.JobSkin(ctx, res)
	if err != nil {
		t.Fatalf("JobSkin() error = %v", err)
	}
	if skin.Texture.Data.Value == "" || skin.Texture.Data.Signature == "" {
		t.Error("texture value or signature is empty")
	}
	if skin.Name != name {
		t.Errorf("Skin.Name = %q, want %q", skin.Name, name)
	}

	t.Run("list", func(t *testing.T) {
		list, err := c.ListJobs(ctx)
		if err != nil {
			t.Fatalf("ListJobs() error = %v", err)
		}
		if len(list.Jobs) == 0 {
			t.Fatal("ListJobs() returned no jobs")
		}
		for _, j := range list.Jobs {
			if j.ID == q.Job.ID {
				if !j.Status.IsDone() {
					t.Errorf("listed job status = %q, want a done status", j.Status)
				}
				return
			}
		}
		t.Errorf("job %s not found in ListJobs()", q.Job.ID)
	})
}

func TestLiveGenerateUpload(t *testing.T) {
	c := sharedGenClient(t)
	name := randomSkinName(t)

	res, err := c.Generate(liveGenContext(t), FromImage(randomSkinImage()),
		&GenerateOptions{Visibility: VisibilityUnlisted, Name: name})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if res.Skin.Texture.Data.Value == "" || res.Skin.Texture.Data.Signature == "" {
		t.Error("texture value or signature is empty")
	}
	if res.Skin.Name != name {
		t.Errorf("Skin.Name = %q, want %q", res.Skin.Name, name)
	}
}

// isStatus429 reports whether err is an API or unexpected-response error with
// HTTP status 429.
func isStatus429(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests {
		return true
	}
	var unexpected *UnexpectedResponseError
	return errors.As(err, &unexpected) && unexpected.Status == http.StatusTooManyRequests
}

func TestLiveThrottleSmoke(t *testing.T) {
	c := sharedGenClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), liveSmokeTimeout)
	t.Cleanup(cancel)

	var last time.Time
	for i := range liveSmokeCalls {
		start := time.Now()
		res, err := c.Generate(ctx, FromImage(randomSkinImage()),
			&GenerateOptions{Visibility: VisibilityUnlisted, Name: randomSkinName(t)})
		if isStatus429(err) {
			t.Fatalf("Generate() #%d got HTTP 429 despite the throttle: %v", i, err)
		}
		if err != nil {
			t.Fatalf("Generate() #%d error = %v", i, err)
		}
		gap := time.Duration(0)
		if !last.IsZero() {
			gap = start.Sub(last)
		}
		last = start
		if res.RateLimit != nil && res.RateLimit.Next != nil {
			t.Logf("call %d: gap since previous start=%s took=%s next=%+v", i, gap, time.Since(start), *res.RateLimit.Next)
		} else {
			t.Logf("call %d: gap since previous start=%s took=%s next=none", i, gap, time.Since(start))
		}
	}
}

func TestLiveAutoTuning(t *testing.T) {
	if os.Getenv("MINESKIN_LIVE_PAID") != "1" {
		t.Skip("MINESKIN_LIVE_PAID=1 not set")
	}
	key := os.Getenv("MINESKIN_API_KEY")
	if strings.TrimSpace(key) == "" {
		t.Skip("MINESKIN_API_KEY not set")
	}
	c, err := NewClient(WithAPIKey(key), WithUserAgent("mineskin-go-client-tests"), WithAutoTuning())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Queue(liveGenContext(t), FromURL(fmt.Sprintf("https://api.mineskin.org/random-image?t=%d", time.Now().Unix())),
		&GenerateOptions{Visibility: VisibilityUnlisted, Name: randomSkinName(t)})
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	tn := c.genGroup.tuner
	tn.mu.Lock()
	settled := tn.settled
	tn.mu.Unlock()
	if !settled {
		t.Fatal("grants were not loaded after the first Queue")
	}
	l := tn.limits(c.clock.Now())
	t.Logf("tuned limits: interval=%s concurrency=%d", l.Interval, l.Concurrency)
	if l.Concurrency <= livePaidMinConcur {
		t.Errorf("tuned concurrency = %d, want > %d", l.Concurrency, livePaidMinConcur)
	}
}
