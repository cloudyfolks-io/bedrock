package release

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadTransportTimeouts(t *testing.T) {
	transport := downloadClient.Transport.(*http.Transport)
	if transport.TLSHandshakeTimeout != 60*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 60*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s", transport.ResponseHeaderTimeout)
	}
	if transport.Proxy == nil {
		t.Fatal("proxy from environment is not honored")
	}
}

func TestDownloadDelays(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(downloadDelays) != len(want) {
		t.Fatalf("delays = %v", downloadDelays)
	}
	for i := range want {
		if downloadDelays[i] != want[i] {
			t.Fatalf("delays = %v", downloadDelays)
		}
	}
}

func TestRetryable(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"server error":   {statusError{code: 503}, true},
		"not found":      {statusError{code: 404}, false},
		"forbidden":      {statusError{code: 403}, false},
		"unexpected eof": {io.ErrUnexpectedEOF, true},
		"canceled":       {context.Canceled, false},
		"other":          {errors.New("disk full"), false},
	}
	for name, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v", name, got)
		}
	}
}

func readAll(r io.Reader) (string, error) {
	body, err := io.ReadAll(r)
	return string(body), err
}

func TestFetchRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		io.WriteString(w, "payload")
	}))
	defer server.Close()
	var got string
	err := fetch(context.Background(), server.Client(), []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, server.URL, func(r io.Reader) (err error) {
		got, err = readAll(r)
		return err
	})
	if err != nil || got != "payload" || calls.Load() != 3 {
		t.Fatalf("err=%v got=%q calls=%d", err, got, calls.Load())
	}
}

func TestFetchDoesNotRetryNotFound(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()
	err := fetch(context.Background(), server.Client(), []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, server.URL, func(io.Reader) error { return nil })
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestFetchGivesUpAfterFourAttempts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := fetch(context.Background(), server.Client(), []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, server.URL, func(io.Reader) error { return nil })
	if err == nil || calls.Load() != 4 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestFetchRetriesNetworkErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "100")
			io.WriteString(w, "short")
			return
		}
		io.WriteString(w, "payload")
	}))
	defer server.Close()
	var got string
	err := fetch(context.Background(), server.Client(), []time.Duration{time.Millisecond}, server.URL, func(r io.Reader) (err error) {
		got, err = readAll(r)
		return err
	})
	if err != nil || got != "payload" {
		t.Fatalf("err=%v got=%q", err, got)
	}
}
