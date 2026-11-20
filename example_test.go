package mineskin_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

const exampleSkinID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

var exampleSkinUUID, _ = mineskin.ParseUUID(exampleSkinID)

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
		case "/v2/me":
			_, _ = fmt.Fprintf(w, `{"success":true,"user":%q,"grants":{"per_minute":10,"concurrency":2}}`, exampleSkinID)
		default:
			if strings.HasPrefix(r.URL.Path, "/v2/skins/") {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":"skin_not_found","message":"Skin not found"}]}`)
				return
			}
			http.NotFound(w, r)
		}
	}))
}

// Example shows the typical flow: create a client, close it when done and
// look up a skin.
func Example() {
	srv := exampleServer()
	defer srv.Close()

	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	res, err := c.GetSkin(context.Background(), exampleSkinUUID)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Skin.Name)
	// Output: Steve
}

func ExampleNewClient() {
	srv := exampleServer()
	defer srv.Close()

	// The API key is read from the environment, never hard-coded. WithBaseURL
	// points the example at a fake server; real programs keep the default.
	c, err := mineskin.NewClient(
		mineskin.WithAPIKey(os.Getenv("MINESKIN_API_KEY")),
		mineskin.WithUserAgent("example/1.0"),
		mineskin.WithTimeout(5*time.Second),
		mineskin.WithBaseURL(srv.URL),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	// Invalid options are reported by NewClient and match ErrInvalidInput.
	_, err = mineskin.NewClient(mineskin.WithTimeout(-time.Second))
	fmt.Println(errors.Is(err, mineskin.ErrInvalidInput))
	// Output: true
}

func ExampleClient_GetSkin() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	// A skin id is a UUID; ParseUUID accepts the dashed and undashed forms.
	id, err := mineskin.ParseUUID(exampleSkinID)
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := c.GetSkin(context.Background(), id)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Skin.Name, res.Meta.Status)
	// Output: Steve 200
}

func ExampleClient_GetUser() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	res, err := c.GetUser(context.Background())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.User.UUID, res.User.Grants.PerMinute, res.User.Grants.Concurrency)
	// Output: 744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11 10 2
}

func ExampleAPIError() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	// The server answers 404 with a MineSkin error body for an unknown skin.
	var unknown mineskin.UUID
	unknown[0] = 1
	_, err = c.GetSkin(context.Background(), unknown)

	if apiErr, ok := errors.AsType[*mineskin.APIError](err); ok {
		fmt.Println(apiErr.Meta.Status, apiErr.Code, apiErr.Message)
	}
	fmt.Println(errors.Is(err, mineskin.ErrNotFound), mineskin.IsRetryable(err))
	// Output:
	// 404 skin_not_found Skin not found
	// true false
}

func ExampleClient_WaitForJob() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	q, err := c.Queue(ctx, mineskin.FromURL("https://example.com/skin.png"), mineskin.GenerateOptions{})
	if err != nil {
		fmt.Println(err)
		return
	}
	done, err := c.WaitForJob(ctx, q.Job)
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := c.GetSkin(ctx, done.Job.Result)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Skin.Name)
	// Output: Steve
}

func ExampleClient_Generate() {
	srv := exampleServer()
	defer srv.Close()
	c, err := mineskin.NewClient(mineskin.WithBaseURL(srv.URL), mineskin.WithUserAgent("example/1.0"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	res, err := c.Generate(context.Background(), mineskin.FromURL("https://example.com/skin.png"),
		mineskin.GenerateOptions{Visibility: mineskin.VisibilityUnlisted})
	if err != nil {
		fmt.Println(err)
		return
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
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()

	res, err := c.Generate(context.Background(), mineskin.FromURL("https://example.com/skin.png"), mineskin.GenerateOptions{})
	if err != nil {
		fmt.Println(err)
		return
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
		fmt.Println(err)
		return
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
		fmt.Println(err)
		return
	}
	if err := c.Close(); err != nil {
		fmt.Println(err)
		return
	}
	// Calls after Close fail immediately.
	_, err = c.GetSkin(context.Background(), exampleSkinUUID)
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
		fmt.Println(err)
		return
	}
	defer func() { _ = c.Close() }()
	skin, err := c.GetSkin(context.Background(), exampleSkinUUID)
	if err != nil {
		fmt.Println(err)
		return
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
