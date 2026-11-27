package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const downloadTimeout = 60 * time.Second

var (
	downloadTransport = newDownloadTransport()
	downloadClient    = &http.Client{Transport: downloadTransport}
	downloadDelays    = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
)

type statusError struct {
	url  string
	code int
}

func (e statusError) Error() string {
	return fmt.Sprintf("download %s: status %d", e.url, e.code)
}

func newDownloadTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = downloadTimeout
	transport.ResponseHeaderTimeout = downloadTimeout
	return transport
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var status statusError
	if errors.As(err, &status) {
		return status.code >= http.StatusInternalServerError
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func fetch(ctx context.Context, client *http.Client, delays []time.Duration, url string, consume func(io.Reader) error) error {
	for attempt := 0; ; attempt++ {
		err := fetchOnce(ctx, client, url, consume)
		if err == nil || attempt >= len(delays) || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delays[attempt]):
		}
	}
}

func fetchOnce(ctx context.Context, client *http.Client, url string, consume func(io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError{url: url, code: resp.StatusCode}
	}
	return consume(resp.Body)
}
