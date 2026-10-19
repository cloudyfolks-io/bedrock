package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	iamVersion     = "2010-05-08"
	iamContentType = "application/x-www-form-urlencoded"
)

func (c *Client) iamCall(ctx context.Context, endpoint, region string, creds Credentials, action, user string, values url.Values) (answer, error) {
	form := url.Values{"Action": {action}, "UserName": {user}, "Version": {iamVersion}}
	for key, value := range values {
		form[key] = value
	}
	return c.do(ctx, call{
		operation: action, method: http.MethodPost, endpoint: endpoint, region: region, service: "iam", path: "/", subject: user,
		headers: map[string]string{"Content-Type": iamContentType}, creds: creds, body: []byte(form.Encode()),
	})
}

func (c *Client) PutUserPolicy(ctx context.Context, endpoint, region string, creds Credentials, user, name, document string) error {
	got, err := c.iamCall(ctx, endpoint, region, creds, "PutUserPolicy", user, url.Values{"PolicyName": {name}, "PolicyDocument": {document}})
	if err != nil {
		return err
	}
	return failure(got, user, "PutUserPolicy")
}

func (c *Client) GetUserPolicy(ctx context.Context, endpoint, region string, creds Credentials, user, name string) (string, bool, error) {
	got, err := c.iamCall(ctx, endpoint, region, creds, "GetUserPolicy", user, url.Values{"PolicyName": {name}})
	if err != nil {
		return "", false, err
	}
	if got.code == NoSuchEntity {
		return "", false, nil
	}
	if err := failure(got, user, "GetUserPolicy"); err != nil {
		return "", false, err
	}
	var result struct {
		Document string `xml:"GetUserPolicyResult>PolicyDocument"`
	}
	if err := xml.Unmarshal(got.body, &result); err != nil {
		return "", false, fmt.Errorf("s3 GetUserPolicy %s: unreadable answer", user)
	}
	return decodeDocument(result.Document), true, nil
}

func (c *Client) DeleteUserPolicy(ctx context.Context, endpoint, region string, creds Credentials, user, name string) error {
	got, err := c.iamCall(ctx, endpoint, region, creds, "DeleteUserPolicy", user, url.Values{"PolicyName": {name}})
	if err != nil {
		return err
	}
	if got.code == NoSuchEntity {
		return nil
	}
	return failure(got, user, "DeleteUserPolicy")
}

func decodeDocument(document string) string {
	trimmed := strings.TrimSpace(document)
	if strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	decoded, err := url.QueryUnescape(trimmed)
	if err != nil {
		return trimmed
	}
	return decoded
}
