package mineskin_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/IvanNovR/mineskin-go-client"
)

const exampleSkinID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

// exampleServer fakes the MineSkin API so the examples run without a network.
func exampleServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/queue":
			fmt.Fprint(w, `{"success":true,"job":{"id":"job1","status":"waiting"}}`)
		case "/v2/queue/job1":
			fmt.Fprintf(w, `{"success":true,"job":{"id":"job1","status":"completed","result":%q}}`, exampleSkinID)
		case "/v2/generate", "/v2/skins/" + exampleSkinID:
			fmt.Fprintf(w, `{"success":true,"skin":{"uuid":%q,"name":"Steve"}}`, exampleSkinID)
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
