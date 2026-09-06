package release

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestResolve(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.Path, "/latest"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization header = %q", got)
		}
		return response(`{"tag_name":"v2.999.0","assets":[{"name":"actions-runner-linux-x64-2.999.0.tar.gz","browser_download_url":"https://example.test/runner.tgz","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`), nil
	})}

	got, err := (Resolver{Client: client, APIBase: "https://api.example.test", Token: "secret"}).Resolve(context.Background(), "latest", "x64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "2.999.0" || got.URL != "https://example.test/runner.tgz" || got.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("unexpected artifact: %#v", got)
	}
}

func TestResolveRequiresDigest(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"tag_name":"v2.999.0","assets":[{"name":"actions-runner-linux-x64-2.999.0.tar.gz","browser_download_url":"https://example.test/runner.tgz"}]}`), nil
	})}

	if _, err := (Resolver{Client: client, APIBase: "https://api.example.test"}).Resolve(context.Background(), "latest", "x64"); err == nil {
		t.Fatal("expected missing digest error")
	}
}

func response(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
