package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Minimal S3-compatible client (R2) over stdlib HTTP + SigV4.
// Only the operations the archiver needs: PUT object, DELETE object.

type r2Client struct {
	http     *http.Client
	endpoint string // e.g. https://<account>.r2.cloudflarestorage.com
	bucket   string
	region   string
	access   string
	secret   string
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func (c *r2Client) sign(req *http.Request, payloadHash string, t time.Time) {
	amzDate := t.UTC().Format("20060102T150405Z")
	dateStamp := t.UTC().Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	var canonicalHeaders string
	for _, h := range signedHeaders {
		canonicalHeaders += h + ":" + strings.TrimSpace(req.Header.Get(h)) + "\n"
	}
	// Include content-type / cache-control in signature when present.
	extra := []string{}
	for _, h := range []string{"content-type", "cache-control"} {
		if v := req.Header.Get(h); v != "" {
			extra = append(extra, h)
		}
	}
	sort.Strings(extra)
	for _, h := range extra {
		signedHeaders = append(signedHeaders, h)
		canonicalHeaders += h + ":" + strings.TrimSpace(req.Header.Get(h)) + "\n"
	}

	uri := req.URL.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	canonical := req.Method + "\n" + uri + "\n\n" + canonicalHeaders + "\n" +
		strings.Join(signedHeaders, ";") + "\n" + payloadHash
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.region)
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + sha256Hex([]byte(canonical))

	kDate := hmacSHA256([]byte("AWS4"+c.secret), dateStamp)
	kRegion := hmacSHA256(kDate, c.region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.access, credentialScope, strings.Join(signedHeaders, ";"), signature))
}

func (c *r2Client) url(key string) string {
	return strings.TrimSuffix(c.endpoint, "/") + "/" + c.bucket + "/" + key
}

// put uploads body with the given content type and cache-control.
func (c *r2Client) put(key, contentType, cacheControl string, body []byte) (int64, error) {
	start := time.Now()
	payloadHash := sha256Hex(body)
	req, err := http.NewRequest("PUT", c.url(key), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	if cacheControl != "" {
		req.Header.Set("Cache-Control", cacheControl)
	}
	c.sign(req, payloadHash, time.Now())
	res, err := c.http.Do(req)
	if err != nil {
		return time.Since(start).Milliseconds(), err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return time.Since(start).Milliseconds(), fmt.Errorf("PUT %s -> HTTP %d", key, res.StatusCode)
	}
	return time.Since(start).Milliseconds(), nil
}

// del deletes key; a missing key (404/NoSuchKey) is not an error.
func (c *r2Client) del(key string) error {
	payloadHash := sha256Hex(nil)
	req, err := http.NewRequest("DELETE", c.url(key), nil)
	if err != nil {
		return err
	}
	c.sign(req, payloadHash, time.Now())
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("DELETE %s -> HTTP %d", key, res.StatusCode)
	}
	return nil
}
