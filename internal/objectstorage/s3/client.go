package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	VersioningEnabled   = "Enabled"
	VersioningSuspended = "Suspended"

	noSuchBucketPolicy = "NoSuchBucketPolicy"
	requestTimeout     = 30 * time.Second
	versioningXMLFmt   = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>%s</Status></VersioningConfiguration>`
)

type Client struct {
	HTTP *http.Client
	Now  func() time.Time
}

type UnavailableError struct{ message string }

func (e *UnavailableError) Error() string { return e.message }

func IsUnavailable(err error) bool {
	var unavailable *UnavailableError
	return errors.As(err, &unavailable)
}

type call struct {
	operation, method, endpoint, region, bucket, query string
	creds                                              Credentials
	body                                               []byte
}

type answer struct {
	status int
	code   string
	body   []byte
}

func (c *Client) GetVersioning(ctx context.Context, endpoint, region string, creds Credentials, bucket string) (string, error) {
	got, err := c.do(ctx, call{operation: "get-versioning", method: http.MethodGet, endpoint: endpoint, region: region, bucket: bucket, query: "versioning", creds: creds})
	if err != nil {
		return "", err
	}
	if err := failure(got, bucket, "get-versioning"); err != nil {
		return "", err
	}
	var configuration struct {
		Status string `xml:"Status"`
	}
	if err := xml.Unmarshal(got.body, &configuration); err != nil {
		return "", fmt.Errorf("s3 get-versioning %s: unreadable answer", bucket)
	}
	return configuration.Status, nil
}

func (c *Client) PutVersioning(ctx context.Context, endpoint, region string, creds Credentials, bucket, status string) error {
	body := []byte(fmt.Sprintf(versioningXMLFmt, status))
	got, err := c.do(ctx, call{operation: "put-versioning", method: http.MethodPut, endpoint: endpoint, region: region, bucket: bucket, query: "versioning", creds: creds, body: body})
	if err != nil {
		return err
	}
	return failure(got, bucket, "put-versioning")
}

func (c *Client) GetPolicy(ctx context.Context, endpoint, region string, creds Credentials, bucket string) (string, bool, error) {
	got, err := c.do(ctx, call{operation: "get-policy", method: http.MethodGet, endpoint: endpoint, region: region, bucket: bucket, query: "policy", creds: creds})
	if err != nil {
		return "", false, err
	}
	if got.code == noSuchBucketPolicy {
		return "", false, nil
	}
	if err := failure(got, bucket, "get-policy"); err != nil {
		return "", false, err
	}
	return string(got.body), true, nil
}

func (c *Client) PutPolicy(ctx context.Context, endpoint, region string, creds Credentials, bucket, policy string) error {
	got, err := c.do(ctx, call{operation: "put-policy", method: http.MethodPut, endpoint: endpoint, region: region, bucket: bucket, query: "policy", creds: creds, body: []byte(policy)})
	if err != nil {
		return err
	}
	return failure(got, bucket, "put-policy")
}

func (c *Client) DeletePolicy(ctx context.Context, endpoint, region string, creds Credentials, bucket string) error {
	got, err := c.do(ctx, call{operation: "delete-policy", method: http.MethodDelete, endpoint: endpoint, region: region, bucket: bucket, query: "policy", creds: creds})
	if err != nil {
		return err
	}
	if got.status == http.StatusNotFound || got.code == noSuchBucketPolicy {
		return nil
	}
	return failure(got, bucket, "delete-policy")
}

func (c *Client) do(ctx context.Context, in call) (answer, error) {
	if in.bucket == "" {
		return answer{}, errors.New("s3: bucket name is empty")
	}
	target, err := url.Parse(in.endpoint)
	if err != nil {
		return answer{}, fmt.Errorf("s3 %s %s: endpoint is not a URL", in.operation, in.bucket)
	}
	path := "/" + in.bucket
	sum := sha256.Sum256(in.body)
	hash := hex.EncodeToString(sum[:])
	signed := Sign(Request{Method: in.method, Host: target.Host, Path: path, RawQuery: in.query, PayloadSHA256: hash}, in.creds, in.region, c.now())
	request, err := http.NewRequestWithContext(ctx, in.method, target.Scheme+"://"+target.Host+canonicalPath(path)+"?"+in.query, bytes.NewReader(in.body))
	if err != nil {
		return answer{}, fmt.Errorf("s3 %s %s: request cannot be built", in.operation, in.bucket)
	}
	for name, value := range signed {
		request.Header.Set(name, value)
	}
	response, err := c.client().Do(request)
	if err != nil {
		return answer{}, &UnavailableError{message: fmt.Sprintf("s3 %s %s: store unreachable", in.operation, in.bucket)}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return answer{}, &UnavailableError{message: fmt.Sprintf("s3 %s %s: answer cannot be read", in.operation, in.bucket)}
	}
	return answer{status: response.StatusCode, code: errorCode(response.StatusCode, body), body: body}, nil
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func errorCode(status int, body []byte) string {
	if status < http.StatusBadRequest {
		return ""
	}
	var document struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &document); err != nil {
		return ""
	}
	return document.Code
}

func failure(got answer, bucket, operation string) error {
	if got.status >= http.StatusOK && got.status < http.StatusMultipleChoices {
		return nil
	}
	message := fmt.Sprintf("s3 %s %s: %d %s", operation, bucket, got.status, got.code)
	if got.status >= http.StatusInternalServerError {
		return &UnavailableError{message: message}
	}
	return errors.New(message)
}
