package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const listPageSize = 1000

type Object struct {
	Key  string
	Size int64
}

type ObjectPage struct {
	Objects   []Object
	Truncated bool
	NextToken string
}

func (c *Client) ListObjectsV2(ctx context.Context, endpoint, region string, creds Credentials, bucket, prefix, token string) (ObjectPage, error) {
	parts := []string{}
	if token != "" {
		parts = append(parts, "continuation-token="+uriEncode(token))
	}
	parts = append(parts, "list-type=2", fmt.Sprintf("max-keys=%d", listPageSize))
	if prefix != "" {
		parts = append(parts, "prefix="+uriEncode(prefix))
	}
	got, err := c.bucketCall(ctx, "list-objects", http.MethodGet, endpoint, region, creds, bucket, strings.Join(parts, "&"), nil)
	if err != nil {
		return ObjectPage{}, err
	}
	if err := failure(got, bucket, "list-objects"); err != nil {
		return ObjectPage{}, err
	}
	var result struct {
		Truncated bool   `xml:"IsTruncated"`
		NextToken string `xml:"NextContinuationToken"`
		Contents  []struct {
			Key  string `xml:"Key"`
			Size int64  `xml:"Size"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(got.body, &result); err != nil {
		return ObjectPage{}, fmt.Errorf("s3 list-objects %s: unreadable answer", bucket)
	}
	page := ObjectPage{Truncated: result.Truncated, NextToken: result.NextToken}
	for _, found := range result.Contents {
		page.Objects = append(page.Objects, Object{Key: found.Key, Size: found.Size})
	}
	return page, nil
}

type PrefixUsage struct {
	Bytes   int64
	Objects int64
}

func (c *Client) UsageOfPrefix(ctx context.Context, endpoint, region string, creds Credentials, bucket, prefix string) (PrefixUsage, error) {
	var usage PrefixUsage
	token := ""
	for {
		page, err := c.ListObjectsV2(ctx, endpoint, region, creds, bucket, prefix, token)
		if err != nil {
			return PrefixUsage{}, err
		}
		for _, object := range page.Objects {
			usage.Bytes += object.Size
			usage.Objects++
		}
		if !page.Truncated || page.NextToken == "" {
			return usage, nil
		}
		token = page.NextToken
	}
}

var ErrPrefixTooBroad = errors.New("a prefix to delete must end with a slash and have at least 8 characters")

func (c *Client) DeletePrefix(ctx context.Context, endpoint, region string, creds Credentials, bucket, prefix string) error {
	if len(prefix) < 8 || !strings.HasSuffix(prefix, "/") {
		return ErrPrefixTooBroad
	}
	for {
		page, err := c.ListObjectsV2(ctx, endpoint, region, creds, bucket, prefix, "")
		if err != nil {
			return err
		}
		if len(page.Objects) == 0 {
			return nil
		}
		versions := make([]ObjectVersion, 0, len(page.Objects))
		for _, object := range page.Objects {
			versions = append(versions, ObjectVersion{Key: object.Key})
		}
		if err := c.DeleteObjects(ctx, endpoint, region, creds, bucket, versions); err != nil {
			return err
		}
	}
}
