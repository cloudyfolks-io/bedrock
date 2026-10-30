package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	attempts       = 4
	firstDelay     = time.Second
	emptyPayload   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	timeLayout     = "20060102T150405Z"
	dateLayout     = "20060102"
	signedHeaders  = "host;x-amz-content-sha256;x-amz-date"
	signingService = "s3"
)

type Client struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	Now       func() time.Time
	Sleep     func(time.Duration)
}

type request struct {
	method  string
	key     string
	query   url.Values
	file    string
	payload string
}

func (c Client) Put(ctx context.Context, key, file string) error {
	sum, err := release.FileSHA256(file)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, request{method: http.MethodPut, key: key, file: file, payload: sum})
	return err
}

func (c Client) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, request{method: http.MethodDelete, key: key, payload: emptyPayload})
	return err
}

func (c Client) List(ctx context.Context, prefix string) ([]string, error) {
	query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
	body, err := c.do(ctx, request{method: http.MethodGet, query: query, payload: emptyPayload})
	if err != nil {
		return nil, err
	}
	var result struct {
		Contents []struct{ Key string }
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("list %s: %w", prefix, err)
	}
	keys := make([]string, 0, len(result.Contents))
	for _, item := range result.Contents {
		keys = append(keys, item.Key)
	}
	return keys, nil
}

func (c Client) do(ctx context.Context, req request) ([]byte, error) {
	var last error
	for attempt := range attempts {
		if attempt > 0 {
			c.Sleep(firstDelay << (attempt - 1))
		}
		body, retry, err := c.once(ctx, req)
		if err == nil || !retry {
			return body, err
		}
		last = err
	}
	return nil, last
}

func (c Client) once(ctx context.Context, req request) ([]byte, bool, error) {
	httpReq, closeBody, err := c.build(ctx, req)
	if err != nil {
		return nil, false, err
	}
	defer closeBody()
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode >= 300 {
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return nil, retry, fmt.Errorf("%s %s: status %d: %s", req.method, req.key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, false, nil
}

func (c Client) build(ctx context.Context, req request) (*http.Request, func(), error) {
	target, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, nil, err
	}
	target.Path = "/" + c.Bucket
	if req.key != "" {
		target.Path += "/" + req.key
	}
	target.RawQuery = strings.ReplaceAll(req.query.Encode(), "+", "%20")
	var body io.Reader
	closeBody := func() {}
	size := int64(0)
	if req.file != "" {
		file, err := os.Open(req.file)
		if err != nil {
			return nil, nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, nil, err
		}
		body, closeBody, size = file, func() { file.Close() }, info.Size()
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, target.String(), body)
	if err != nil {
		closeBody()
		return nil, nil, err
	}
	if body != nil {
		httpReq.ContentLength = size
	}
	c.sign(httpReq, target, req.payload)
	return httpReq, closeBody, nil
}

func (c Client) sign(req *http.Request, target *url.URL, payload string) {
	now := c.Now().UTC()
	stamp := now.Format(timeLayout)
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", payload)
	canonical := strings.Join([]string{
		req.Method,
		target.EscapedPath(),
		target.RawQuery,
		"host:" + target.Host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + stamp + "\n",
		signedHeaders,
		payload,
	}, "\n")
	scope := strings.Join([]string{now.Format(dateLayout), c.Region, signingService, "aws4_request"}, "/")
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", stamp, scope, hexSum(canonical)}, "\n")
	key := []byte("AWS4" + c.SecretKey)
	for _, part := range []string{now.Format(dateLayout), c.Region, signingService, "aws4_request"} {
		key = mac(key, part)
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signedHeaders, hex.EncodeToString(mac(key, toSign))))
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSum(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}
