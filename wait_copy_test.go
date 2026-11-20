package mineskin

import (
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
)

const (
	richCompleted = `{"success":true,` +
		`"job":{"id":"j1","status":"completed","result":"` + jobResultID + `"},` +
		`"skin":{"uuid":"` + jobResultID + `","name":"steve","variant":"slim","visibility":"public",` +
		`"texture":{"data":{"value":"val","signature":"sig"},"hash":{"skin":"hs","cape":"hc"},"url":{"skin":"us","cape":"uc"}}},` +
		`"warnings":[{"code":"w1","message":"warn one"},{"code":"w2","message":"warn two"}],` +
		`"messages":[{"code":"m1","message":"msg one"}],` +
		`"links":{"skin":"https://example.test/s"}}`
	richFailed = `{"success":true,` +
		`"job":{"id":"j1","status":"failed"},` +
		`"errors":[{"code":"e1","message":"err one"},{"code":"e2","message":"err two"}],` +
		`"warnings":[{"code":"w1","message":"warn one"}],` +
		`"messages":[{"code":"m1","message":"msg one"}],` +
		`"links":{"skin":"https://example.test/s"}}`
)

// waitTwice makes two waiters wait for the same job, answered with finalBody,
// and returns both outcomes.
func waitTwice(t *testing.T, finalBody string) (resp [2]*JobResponse, errs [2]error) {
	t.Helper()
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("MineSkin-Breadcrumb", "crumb")
		w.Header().Set("X-MineSkin-Extra", "extra")
		writeJSON(200, finalBody)(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())
	var wg sync.WaitGroup
	for i := range resp {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp[i], errs[i] = c.WaitForJob(waitCtx(t), Job{ID: "j1"})
		}()
	}
	eventually(t, "both waiters registered", func() bool { return c.poller.Waiters("j1") == 2 })
	close(release)
	wg.Wait()
	return resp, errs
}

// scribble overwrites everything reachable from m, in place.
func scribble(m *ResponseMeta) {
	for k := range m.Header {
		for i := range m.Header[k] {
			m.Header[k][i] = "mutated"
		}
		m.Header[k] = append(m.Header[k], "added")
	}
	m.Header.Set("Injected", "yes")
	for i := range m.Warnings {
		m.Warnings[i] = CodeAndMessage{Code: "mutated", Message: "mutated"}
	}
	for i := range m.Messages {
		m.Messages[i] = CodeAndMessage{Code: "mutated", Message: "mutated"}
	}
	for i := range m.Links {
		m.Links[i] = 'X'
	}
}

func TestWaitForJobResultsAreIndependentOnCompletion(t *testing.T) {
	t.Parallel()
	resp, errs := waitTwice(t, richCompleted)
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors = %v, %v", errs[0], errs[1])
	}
	pristine := *resp[1]
	pristineSkin := *resp[1].Skin
	pristine.Skin = &pristineSkin
	pristine.Meta = cloneMetaForTest(resp[1].Meta)

	a := resp[0]
	scribble(&a.Meta)
	a.Skin.Name = "mutated"
	a.Skin.Variant = VariantClassic
	a.Skin.Texture.Data.Value = "mutated"
	a.Skin.Texture.URL.Skin = "mutated"
	a.Job.ID = "mutated"

	b := resp[1]
	if b.Skin == a.Skin {
		t.Fatal("waiters share one *Skin")
	}
	if !reflect.DeepEqual(*b, pristine) {
		t.Errorf("second waiter sees the first waiter's changes:\n got  %+v\n want %+v", *b, pristine)
	}
	if got := b.Meta.Header.Get("Mineskin-Breadcrumb"); got != "crumb" || b.Meta.Header.Get("Injected") != "" {
		t.Errorf("header = %v, want the original with no injected key", b.Meta.Header)
	}
	if string(b.Meta.Links) != `{"skin":"https://example.test/s"}` || b.Skin.Name != "steve" || b.Skin.Texture.Data.Value != "val" {
		t.Errorf("links = %s, skin = %+v; want the originals", b.Meta.Links, b.Skin)
	}
	if b.Meta.Warnings[1].Code != "w2" || b.Meta.Messages[0].Message != "msg one" {
		t.Errorf("warnings = %+v, messages = %+v; want the originals", b.Meta.Warnings, b.Meta.Messages)
	}
}

func TestWaitForJobResultsAreIndependentOnFailure(t *testing.T) {
	t.Parallel()
	_, errs := waitTwice(t, richFailed)
	var a, b *JobFailedError
	if !errors.As(errs[0], &a) || !errors.As(errs[1], &b) {
		t.Fatalf("errors = %v, %v; want *JobFailedError twice", errs[0], errs[1])
	}
	wantErrs := []CodeAndMessage{{"e1", "err one"}, {"e2", "err two"}}
	scribble(&a.Meta)
	a.Errors[0] = CodeAndMessage{Code: "mutated", Message: "mutated"}
	a.Errors[1].Message = "mutated"
	if !reflect.DeepEqual(b.Errors, wantErrs) {
		t.Errorf("Errors = %+v, want %+v", b.Errors, wantErrs)
	}
	if b.Meta.Warnings[0].Code != "w1" || b.Meta.Header.Get("Injected") != "" || string(b.Meta.Links) != `{"skin":"https://example.test/s"}` {
		t.Errorf("meta = %+v, want the originals", b.Meta)
	}
}

// cloneMetaForTest deep-copies m with plain loops, independently of the code under test.
func cloneMetaForTest(m ResponseMeta) ResponseMeta {
	out := m
	out.Header = http.Header{}
	for k, v := range m.Header {
		out.Header[k] = append([]string(nil), v...)
	}
	out.Warnings = append([]CodeAndMessage(nil), m.Warnings...)
	out.Messages = append([]CodeAndMessage(nil), m.Messages...)
	out.Links = append([]byte(nil), m.Links...)
	return out
}
