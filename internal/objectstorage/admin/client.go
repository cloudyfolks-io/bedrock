package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

const (
	requestTimeout = 30 * time.Second
	unlimited      = -1
)

type Client struct {
	HTTP *http.Client
	Now  func() time.Time
}

type Stats struct {
	Bytes   int64
	Objects int64
}

func (c *Client) BucketStats(ctx context.Context, endpoint, region string, creds s3.Credentials, bucket string) (Stats, error) {
	if bucket == "" {
		return Stats{}, errors.New("admin: bucket name is empty")
	}
	query := url.Values{"bucket": {bucket}, "stats": {"true"}}
	var answer struct {
		Usage map[string]struct {
			SizeActual int64 `json:"size_actual"`
			NumObjects int64 `json:"num_objects"`
		} `json:"usage"`
	}
	if err := c.call(ctx, http.MethodGet, endpoint, region, creds, "/admin/bucket", bucket, query, &answer); err != nil {
		return Stats{}, err
	}
	main := answer.Usage["rgw.main"]
	return Stats{Bytes: main.SizeActual, Objects: main.NumObjects}, nil
}

func (c *Client) SetBucketQuota(ctx context.Context, endpoint, region string, creds s3.Credentials, bucket string, bytes, objects int64) error {
	query := url.Values{"quota": {""}, "bucket": {bucket}, "max-size": {limit(bytes)}, "max-objects": {limit(objects)}, "enabled": {"true"}}
	return c.call(ctx, http.MethodPut, endpoint, region, creds, "/admin/bucket", bucket, query, nil)
}

func (c *Client) RemoveBucketQuota(ctx context.Context, endpoint, region string, creds s3.Credentials, bucket string) error {
	query := url.Values{"quota": {""}, "bucket": {bucket}, "enabled": {"false"}}
	return c.call(ctx, http.MethodPut, endpoint, region, creds, "/admin/bucket", bucket, query, nil)
}

func (c *Client) SuspendUser(ctx context.Context, endpoint, region string, creds s3.Credentials, uid string) error {
	return c.setSuspended(ctx, endpoint, region, creds, uid, "true")
}

func (c *Client) ResumeUser(ctx context.Context, endpoint, region string, creds s3.Credentials, uid string) error {
	return c.setSuspended(ctx, endpoint, region, creds, uid, "false")
}

func (c *Client) setSuspended(ctx context.Context, endpoint, region string, creds s3.Credentials, uid, value string) error {
	if uid == "" {
		return errors.New("admin: user id is empty")
	}
	query := url.Values{"uid": {uid}, "suspended": {value}}
	return c.call(ctx, http.MethodPost, endpoint, region, creds, "/admin/user", uid, query, nil)
}

func limit(value int64) string {
	if value <= 0 {
		return strconv.Itoa(unlimited)
	}
	return strconv.FormatInt(value, 10)
}

func (c *Client) call(ctx context.Context, method, endpoint, region string, creds s3.Credentials, path, subject string, query url.Values, out any) error {
	target, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("admin %s %s: endpoint is not a URL", method, subject)
	}
	query.Set("format", "json")
	raw := query.Encode()
	sum := sha256.Sum256(nil)
	hash := hex.EncodeToString(sum[:])
	signed := s3.Sign(s3.Request{Method: method, Host: target.Host, Path: path, RawQuery: raw, PayloadSHA256: hash}, creds, region, c.now())
	request, err := http.NewRequestWithContext(ctx, method, target.Scheme+"://"+target.Host+path+"?"+raw, nil)
	if err != nil {
		return fmt.Errorf("admin %s %s: request cannot be built", method, subject)
	}
	for name, value := range signed {
		request.Header.Set(name, value)
	}
	response, err := c.client().Do(request)
	if err != nil {
		return s3.Unavailable(fmt.Sprintf("admin %s %s: store unreachable", method, subject))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return s3.Unavailable(fmt.Sprintf("admin %s %s: answer cannot be read", method, subject))
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return failure(response.StatusCode, method, subject, body)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("admin %s %s: unreadable answer", method, subject)
	}
	return nil
}

func failure(status int, method, subject string, body []byte) error {
	var document struct {
		Code string `json:"Code"`
	}
	_ = json.Unmarshal(body, &document)
	failed := &s3.Error{Operation: "admin " + method, Subject: subject, Status: status, Code: document.Code}
	if status >= http.StatusInternalServerError {
		return s3.Unavailable(failed.Error())
	}
	return failed
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
