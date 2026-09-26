package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

const (
	blobIdleTimeout = 60 * time.Second
	blobPauseBase   = time.Second
	blobPauseCap    = 30 * time.Second
	blobAttempts    = 500
	blobStalls      = 3
)

var (
	blobPath   = regexp.MustCompile(`^/v2/.+/blobs/sha256:([a-f0-9]{64})$`)
	errStalled = errors.New("no bytes received within the idle timeout")
)

type blobCache struct {
	dir   string
	next  http.RoundTripper
	idle  time.Duration
	pause time.Duration
}

func (c blobCache) RoundTrip(req *http.Request) (*http.Response, error) {
	match := blobPath.FindStringSubmatch(req.URL.Path)
	if req.Method != http.MethodGet || match == nil {
		return c.next.RoundTrip(req)
	}
	path := filepath.Join(c.dir, "blobs", "sha256", match[1])
	lock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if resp, err := cachedResponse(req, path, match[1]); err == nil {
		return resp, nil
	}
	return c.download(req, path, match[1])
}

func (c blobCache) download(req *http.Request, path, digest string) (*http.Response, error) {
	partial := path + ".partial"
	stalls := 0
	for attempt := 1; ; attempt++ {
		before := fileSize(partial)
		resp, err := c.fetch(req, partial)
		if resp != nil {
			return resp, nil
		}
		if err == nil {
			err = promote(partial, path, digest)
		}
		if err == nil {
			return cachedResponse(req, path, digest)
		}
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		stalls = nextStalls(stalls, before, fileSize(partial))
		if stalls >= blobStalls || attempt >= blobAttempts {
			return nil, fmt.Errorf("blob sha256:%s failed after %d attempts, %d in a row without progress: %v", digest, attempt, stalls, err)
		}
		if err := sleep(req.Context(), backoff(c.pause, stalls)); err != nil {
			return nil, err
		}
	}
}

func (c blobCache) fetch(req *http.Request, partial string) (*http.Response, error) {
	offset := fileSize(partial)
	ctx, cancel := context.WithCancelCause(req.Context())
	defer cancel(nil)
	timer := time.AfterFunc(c.idle, func() { cancel(errStalled) })
	defer timer.Stop()
	out := req.Clone(ctx)
	if offset > 0 {
		out.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := (&http.Client{Transport: c.next}).Do(out)
	if err != nil {
		return nil, stallCause(ctx, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil, stallCause(ctx, writeFrom(partial, 0, resp.Body, timer, c.idle))
	case resp.StatusCode == http.StatusPartialContent && rangeStart(resp) == offset:
		return nil, stallCause(ctx, writeFrom(partial, offset, resp.Body, timer, c.idle))
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0:
		return nil, nil
	case resp.StatusCode == http.StatusPartialContent:
		return nil, errors.Join(fmt.Errorf("Content-Range %q does not start at %d", resp.Header.Get("Content-Range"), offset), os.Remove(partial))
	case retryableStatus(resp.StatusCode):
		return nil, fmt.Errorf("status %s", resp.Status)
	default:
		return buffered(resp)
	}
}

func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func writeFrom(path string, offset int64, body io.Reader, timer *time.Timer, idle time.Duration) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := file.Truncate(offset); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		file.Close()
		return err
	}
	return firstError(copyWhileAlive(file, body, timer, idle), file.Close())
}

func copyWhileAlive(dst io.Writer, src io.Reader, timer *time.Timer, idle time.Duration) error {
	buf := make([]byte, 64<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			timer.Reset(idle)
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func stallCause(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), errStalled) {
		return errStalled
	}
	return err
}

func rangeStart(resp *http.Response) int64 {
	var start int64
	if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-", &start); err != nil {
		return -1
	}
	return start
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

func buffered(resp *http.Response) (*http.Response, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	copied := *resp
	copied.Body = io.NopCloser(bytes.NewReader(body))
	return &copied, nil
}

func promote(partial, path, digest string) error {
	file, err := openVerified(partial, digest)
	if err != nil {
		return errors.Join(err, os.Remove(partial))
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(partial, path)
}

func nextStalls(stalls int, before, after int64) int {
	if after > before {
		return 0
	}
	return stalls + 1
}

func backoff(base time.Duration, stalls int) time.Duration {
	pause := base
	for range stalls {
		pause *= 5
	}
	return min(pause, blobPauseCap)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func cachedResponse(req *http.Request, path, digest string) (*http.Response, error) {
	file, err := openVerified(path, digest)
	if err != nil {
		return nil, err
	}
	return fileResponse(req, file)
}

func openVerified(path, want string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		file.Close()
		return nil, err
	}
	if hex.EncodeToString(sum.Sum(nil)) != want {
		file.Close()
		return nil, fmt.Errorf("%s does not match sha256:%s", path, want)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func fileResponse(req *http.Request, file *os.File) (*http.Response, error) {
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Length": {strconv.FormatInt(info.Size(), 10)}, "Content-Type": {"application/octet-stream"}},
		ContentLength: info.Size(),
		Body:          file,
		Request:       req,
	}, nil
}
