package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Credentials struct{ AccessKey, SecretKey string }

type Request struct {
	Method, Host, Path, RawQuery string
	Headers                      map[string]string
	PayloadSHA256                string
}

const (
	algorithm     = "AWS4-HMAC-SHA256"
	service       = "s3"
	terminator    = "aws4_request"
	amzDateLayout = "20060102T150405Z"
	dateLayout    = "20060102"
)

func Sign(r Request, c Credentials, region string, now time.Time) map[string]string {
	moment := now.UTC()
	amzDate := moment.Format(amzDateLayout)
	headers := signedHeaderValues(r, amzDate)
	names := sortedKeys(headers)
	scope := strings.Join([]string{moment.Format(dateLayout), region, service, terminator}, "/")
	canonical := canonicalRequest(r, headers, names)
	toSign := strings.Join([]string{algorithm, amzDate, scope, hexSHA256(canonical)}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.SecretKey, moment.Format(dateLayout), region), toSign))
	authorization := algorithm + " Credential=" + c.AccessKey + "/" + scope + ",SignedHeaders=" + strings.Join(names, ";") + ",Signature=" + signature
	return map[string]string{
		"Authorization":        authorization,
		"x-amz-date":           amzDate,
		"x-amz-content-sha256": r.PayloadSHA256,
	}
}

func signedHeaderValues(r Request, amzDate string) map[string]string {
	headers := map[string]string{}
	for name, value := range r.Headers {
		headers[strings.ToLower(name)] = strings.TrimSpace(value)
	}
	headers["host"] = r.Host
	headers["x-amz-content-sha256"] = r.PayloadSHA256
	headers["x-amz-date"] = amzDate
	return headers
}

func canonicalRequest(r Request, headers map[string]string, names []string) string {
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, name+":"+headers[name]+"\n")
	}
	return strings.Join([]string{
		r.Method,
		canonicalPath(r.Path),
		canonicalQuery(r.RawQuery),
		strings.Join(lines, ""),
		strings.Join(names, ";"),
		r.PayloadSHA256,
	}, "\n")
}

func canonicalPath(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = uriEncode(segment)
	}
	return strings.Join(segments, "/")
}

func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	pairs := []string{}
	for _, part := range strings.Split(raw, "&") {
		key, value, _ := strings.Cut(part, "=")
		pairs = append(pairs, uriEncode(unescape(key))+"="+uriEncode(unescape(value)))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func unescape(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func uriEncode(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if unreserved(b) {
			out.WriteByte(b)
			continue
		}
		out.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{b})))
	}
	return out.String()
}

func unreserved(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.' || b == '~'
}

func signingKey(secret, date, region string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	return hmacSHA256(key, terminator)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hexSHA256(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
