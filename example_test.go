package mineskin_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

const exampleSkinID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

// exampleServer fakes the MineSkin API so the examples run without a network.
func exampleServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/queue":
			_, _ = fmt.Fprint(w, `{"success":true,"job":{"id":"job1","status":"waiting"}}`)
		case "/v2/queue/job1":
			_, _ = fmt.Fprintf(w, `{"success":true,"job":{"id":"job1","status":"completed","result":%q}}`, exampleSkinID)
		case "/v2/generate", "/v2/skins/" + exampleSkinID:
			_, _ = fmt.Fprintf(w, `{"success":true,"skin":{"uuid":%q,"name":"Steve"}}`, exampleSkinID)
		default:
			http.NotFound(w, r)
		}
	}))
}

func ExampleClient_WaitForJob() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	q, err := c.Queue(ctx, mineskin.FromURL("https://example.com/skin.png"), nil)
	if err != nil {
		log.Fatal(err)
	}
	done, err := c.WaitForJob(ctx, q.Job)
	if err != nil {
		log.Fatal(err)
	}
	skin, err := c.JobSkin(ctx, done)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(skin.Name)
	// Output: Steve
}

func ExampleClient_Generate() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		log.Fatal(err)
	}

	res, err := c.Generate(context.Background(), mineskin.FromURL("https://example.com/skin.png"),
		&mineskin.GenerateOptions{Visibility: mineskin.VisibilityUnlisted})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Skin.Name)
	// Output: Steve
}

func ExampleWithAutoTuning() {
	srv := exampleServer()
	defer srv.Close()
	// The generate group follows the grants of the key; the first Queue or
	// Generate call loads them. A free key or a failed load keeps a safe
	// baseline.
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithAutoTuning())
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	res, err := c.Generate(context.Background(), mineskin.FromURL("https://example.com/skin.png"), nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Skin.Name)
	// Output: Steve
}

func ExampleWithGenerateThrottle() {
	// A slower, stricter generate group. A zero field keeps the default.
	c, err := mineskin.NewClient(
		mineskin.WithGenerateThrottle(mineskin.Throttle{Interval: time.Second, Concurrency: 1}),
		mineskin.WithGetThrottle(mineskin.Throttle{Concurrency: 2}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// A negative field is rejected.
	_, err = mineskin.NewClient(mineskin.WithGetThrottle(mineskin.Throttle{Interval: -time.Second}))
	fmt.Println(errors.Is(err, mineskin.ErrInvalidInput))
	// Output: true
}

func ExampleClient_Close() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL))
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Close(); err != nil {
		log.Fatal(err)
	}
	// Calls after Close fail immediately.
	_, err = c.GetSkin(context.Background(), exampleSkinID)
	fmt.Println(errors.Is(err, mineskin.ErrClientClosed))
	// Output: true
}

func ExampleWithRateLimitRetries() {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":"rate_limit","message":"request too soon"}]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"skin":{"uuid":%q,"name":"Steve"}}`, exampleSkinID)
	}))
	defer srv.Close()

	// The default is two retries; a 429 is retried through the throttle.
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"),
		mineskin.WithRateLimitRetries(2))
	if err != nil {
		log.Fatal(err)
	}
	skin, err := c.GetSkin(context.Background(), exampleSkinID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(skin.Skin.Name, calls.Load())
	// Output: Steve 2
}

func ExampleBackoff() {
	backoff := mineskin.Backoff{
		Initial: 200 * time.Millisecond, Max: 2 * time.Second,
		Multiplier: 2.0, FreeAttempts: 3,
	}
	for _, attempt := range []int{1, 3, 4, 5, 7} {
		fmt.Println(attempt, backoff.Wait(attempt))
	}
	// Output:
	// 1 200ms
	// 3 200ms
	// 4 400ms
	// 5 800ms
	// 7 2s
}
