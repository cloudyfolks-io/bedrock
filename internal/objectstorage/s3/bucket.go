package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

const (
	versionsPageSize = 1000
	corsMaxAge       = 3000
	xmlNamespace     = "http://s3.amazonaws.com/doc/2006-03-01/"
)

var corsMethods = []string{"GET", "PUT", "POST", "DELETE", "HEAD"}

type ObjectVersion struct {
	Key       string
	VersionID string
}

type VersionPage struct {
	Objects             []ObjectVersion
	Truncated           bool
	NextKeyMarker       string
	NextVersionIDMarker string
}

func (c *Client) bucketCall(ctx context.Context, operation, method, endpoint, region string, creds Credentials, bucket, query string, body []byte) (answer, error) {
	var headers map[string]string
	if len(body) > 0 {
		sum := md5.Sum(body)
		headers = map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])}
	}
	return c.do(ctx, call{operation: operation, method: method, endpoint: endpoint, region: region, bucket: bucket, query: query, headers: headers, creds: creds, body: body})
}

func (c *Client) CreateBucket(ctx context.Context, endpoint, region string, creds Credentials, bucket string) error {
	got, err := c.bucketCall(ctx, "create-bucket", http.MethodPut, endpoint, region, creds, bucket, "", nil)
	if err != nil {
		return err
	}
	return failure(got, bucket, "create-bucket")
}

func (c *Client) DeleteBucket(ctx context.Context, endpoint, region string, creds Credentials, bucket string) error {
	got, err := c.bucketCall(ctx, "delete-bucket", http.MethodDelete, endpoint, region, creds, bucket, "", nil)
	if err != nil {
		return err
	}
	return failure(got, bucket, "delete-bucket")
}

func (c *Client) HeadBucket(ctx context.Context, endpoint, region string, creds Credentials, bucket string) error {
	got, err := c.bucketCall(ctx, "head-bucket", http.MethodHead, endpoint, region, creds, bucket, "", nil)
	if err != nil {
		return err
	}
	switch got.status {
	case http.StatusNotFound:
		got.code = NoSuchBucket
	case http.StatusForbidden:
		got.code = AccessDenied
	}
	return failure(got, bucket, "head-bucket")
}

func (c *Client) GetBucketOwner(ctx context.Context, endpoint, region string, creds Credentials, bucket string) (string, error) {
	got, err := c.bucketCall(ctx, "get-acl", http.MethodGet, endpoint, region, creds, bucket, "acl", nil)
	if err != nil {
		return "", err
	}
	if err := failure(got, bucket, "get-acl"); err != nil {
		return "", err
	}
	var policy struct {
		Owner string `xml:"Owner>ID"`
	}
	if err := xml.Unmarshal(got.body, &policy); err != nil {
		return "", fmt.Errorf("s3 get-acl %s: unreadable answer", bucket)
	}
	return policy.Owner, nil
}

func (c *Client) PutBucketCors(ctx context.Context, endpoint, region string, creds Credentials, bucket string, origins []string) error {
	got, err := c.bucketCall(ctx, "put-cors", http.MethodPut, endpoint, region, creds, bucket, "cors", corsBody(origins))
	if err != nil {
		return err
	}
	return failure(got, bucket, "put-cors")
}

func (c *Client) DeleteBucketCors(ctx context.Context, endpoint, region string, creds Credentials, bucket string) error {
	got, err := c.bucketCall(ctx, "delete-cors", http.MethodDelete, endpoint, region, creds, bucket, "cors", nil)
	if err != nil {
		return err
	}
	return failure(got, bucket, "delete-cors")
}

func (c *Client) ListObjectVersions(ctx context.Context, endpoint, region string, creds Credentials, bucket, keyMarker, versionMarker string) (VersionPage, error) {
	parts := []string{}
	if keyMarker != "" {
		parts = append(parts, "key-marker="+uriEncode(keyMarker))
	}
	parts = append(parts, fmt.Sprintf("max-keys=%d", versionsPageSize))
	if versionMarker != "" {
		parts = append(parts, "version-id-marker="+uriEncode(versionMarker))
	}
	parts = append(parts, "versions")
	got, err := c.bucketCall(ctx, "list-versions", http.MethodGet, endpoint, region, creds, bucket, strings.Join(parts, "&"), nil)
	if err != nil {
		return VersionPage{}, err
	}
	if err := failure(got, bucket, "list-versions"); err != nil {
		return VersionPage{}, err
	}
	return parseVersions(got.body, bucket)
}

func (c *Client) DeleteObjects(ctx context.Context, endpoint, region string, creds Credentials, bucket string, objects []ObjectVersion) error {
	if len(objects) == 0 {
		return nil
	}
	got, err := c.bucketCall(ctx, "delete-objects", http.MethodPost, endpoint, region, creds, bucket, "delete", deleteBody(objects))
	if err != nil {
		return err
	}
	if err := failure(got, bucket, "delete-objects"); err != nil {
		return err
	}
	var result struct {
		Errors []struct {
			Code string `xml:"Code"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(got.body, &result); err != nil {
		return fmt.Errorf("s3 delete-objects %s: unreadable answer", bucket)
	}
	if len(result.Errors) > 0 {
		return &Error{Operation: "delete-objects", Subject: bucket, Status: http.StatusOK, Code: result.Errors[0].Code}
	}
	return nil
}

func parseVersions(body []byte, bucket string) (VersionPage, error) {
	type entry struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
	}
	var result struct {
		Truncated     bool    `xml:"IsTruncated"`
		NextKey       string  `xml:"NextKeyMarker"`
		NextVersion   string  `xml:"NextVersionIdMarker"`
		Versions      []entry `xml:"Version"`
		DeleteMarkers []entry `xml:"DeleteMarker"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		return VersionPage{}, fmt.Errorf("s3 list-versions %s: unreadable answer", bucket)
	}
	page := VersionPage{Truncated: result.Truncated, NextKeyMarker: result.NextKey, NextVersionIDMarker: result.NextVersion}
	for _, found := range append(result.Versions, result.DeleteMarkers...) {
		page.Objects = append(page.Objects, ObjectVersion{Key: found.Key, VersionID: found.VersionID})
	}
	return page, nil
}

func corsBody(origins []string) []byte {
	var out bytes.Buffer
	out.WriteString(`<CORSConfiguration xmlns="` + xmlNamespace + `"><CORSRule>`)
	for _, origin := range origins {
		out.WriteString("<AllowedOrigin>" + escapeXML(origin) + "</AllowedOrigin>")
	}
	for _, method := range corsMethods {
		out.WriteString("<AllowedMethod>" + method + "</AllowedMethod>")
	}
	out.WriteString(fmt.Sprintf("<AllowedHeader>*</AllowedHeader><ExposeHeader>ETag</ExposeHeader><MaxAgeSeconds>%d</MaxAgeSeconds></CORSRule></CORSConfiguration>", corsMaxAge))
	return out.Bytes()
}

func deleteBody(objects []ObjectVersion) []byte {
	var out bytes.Buffer
	out.WriteString(`<Delete xmlns="` + xmlNamespace + `"><Quiet>true</Quiet>`)
	for _, object := range objects {
		out.WriteString("<Object><Key>" + escapeXML(object.Key) + "</Key>")
		if object.VersionID != "" {
			out.WriteString("<VersionId>" + escapeXML(object.VersionID) + "</VersionId>")
		}
		out.WriteString("</Object>")
	}
	out.WriteString("</Delete>")
	return out.Bytes()
}

func escapeXML(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}
