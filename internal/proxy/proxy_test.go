package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// catalogue stands in for a gateway, recording the headers of the last request
// so tests can assert on what cpa actually sent.
func catalogue(t *testing.T, body string, status int) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

const twoModels = `{"data":[{"id":"beta-2"},{"id":"alpha-1"}]}`

// The version header is not sent on purpose: on gateways that treat it as a
// model-id switch it hides the real ids behind opaque aliases, which breaks
// family matching. Sending it again would silently undo that.
func TestListModelsSendsNoAnthropicVersion(t *testing.T) {
	srv, seen := catalogue(t, twoModels, http.StatusOK)
	if _, err := New(srv.URL, "k").ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if v := seen.Get("anthropic-version"); v != "" {
		t.Errorf("anthropic-version = %q, want it unset", v)
	}
}

func TestListModelsSendsBothAuthSpellings(t *testing.T) {
	srv, seen := catalogue(t, twoModels, http.StatusOK)
	if _, err := New(srv.URL, "k").ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := seen.Get("x-api-key"); got != "k" {
		t.Errorf("x-api-key = %q, want %q", got, "k")
	}
	if got := seen.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer k")
	}
}

func TestListModelsSendsNoAuthWithoutAKey(t *testing.T) {
	srv, seen := catalogue(t, twoModels, http.StatusOK)
	if _, err := New(srv.URL, "").ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := seen.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q, want it unset", got)
	}
	if got := seen.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want it unset", got)
	}
}

func TestListModelsSortsByID(t *testing.T) {
	srv, _ := catalogue(t, twoModels, http.StatusOK)
	got, err := New(srv.URL, "k").ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 || got[0].ID != "alpha-1" || got[1].ID != "beta-2" {
		t.Fatalf("ids = %v, want [alpha-1 beta-2]", ids(got))
	}
}

func TestListModelsReportsHTTPError(t *testing.T) {
	srv, _ := catalogue(t, `{"error":"nope"}`, http.StatusUnauthorized)
	_, err := New(srv.URL, "k").ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want it to name the status", err)
	}
}

func ids(ms []Model) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
