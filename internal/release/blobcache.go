package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var blobPath = regexp.MustCompile(`^/v2/.+/blobs/sha256:([a-f0-9]{64})$`)

type blobCache struct {
	dir  string
	next http.RoundTripper
}

func (c blobCache) RoundTrip(req *http.Request) (*http.Response, error) {
	match := blobPath.FindStringSubmatch(req.URL.Path)
	if req.Method != http.MethodGet || match == nil {
		return c.next.RoundTrip(req)
	}
	path := filepath.Join(c.dir, "blobs", "sha256", match[1])
	if file, err := openVerified(path, match[1]); err == nil {
		return fileResponse(req, file)
	}
	resp, err := (&http.Client{Transport: c.next}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	if err := storeVerified(resp.Body, path, match[1]); err != nil {
		return nil, err
	}
	file, err := openVerified(path, match[1])
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
		return nil, fmt.Errorf("cached blob %s does not match its digest", path)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func storeVerified(body io.ReadCloser, path, want string) error {
	defer body.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	part, err := os.CreateTemp(filepath.Dir(path), want+".part-")
	if err != nil {
		return err
	}
	defer os.Remove(part.Name())
	sum := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(part, sum), body)
	if err := firstError(copyErr, part.Close()); err != nil {
		return fmt.Errorf("download blob sha256:%s: %w", want, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return fmt.Errorf("download blob sha256:%s: got sha256:%s", want, got)
	}
	return os.Rename(part.Name(), path)
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
