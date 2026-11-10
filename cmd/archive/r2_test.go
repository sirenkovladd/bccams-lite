package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS Signature Version 4 official test vector ("get-vanilla").
// https://docs.aws.amazon.com/general/latest/gr/signature-v4-test-suite.html
func TestSigV4KnownAnswer(t *testing.T) {
	amzDate, dateStamp := "20150830T123600Z", "20150830"
	canonicalHash := "bb579772317eb040ac9ed261061d46c1f17a8133879d6129b6e1c25292927e63"
	got := buildStringToSign(amzDate, dateStamp, "us-east-1", "service", canonicalHash)
	want := "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/service/aws4_request\n" + canonicalHash
	if got != want {
		t.Fatalf("string-to-sign mismatch:\n got %q\nwant %q", got, want)
	}
	key := deriveSigningKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", dateStamp, "us-east-1", "service")
	sig := hexHMAC(key, want)
	if sig != "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31" {
		t.Fatalf("signature mismatch: %s", sig)
	}
}

// The signer still attaches the standard S3 headers end to end.
func TestSignAttachesAuthHeader(t *testing.T) {
	c := &r2Client{endpoint: "https://x.r2.cloudflarestorage.com", bucket: "b",
		region: "auto", access: "AK", secret: "SK"}
	req, _ := http.NewRequest("PUT", c.url("history/a/N/t.jpg"), nil)
	req.Header.Set("Content-Type", "image/jpeg")
	c.sign(req, "abc123", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	auth := req.Header.Get("Authorization")
	for _, want := range []string{"AWS4-HMAC-SHA256 Credential=AK/", "SignedHeaders=", "Signature="} {
		if !contains(auth, want) {
			t.Fatalf("Authorization header missing %q: %q", want, auth)
		}
	}
}

// The signature must verify against the headers actually sent on the wire.
// Regression: sign() read the host via req.Header.Get("host"), which is always
// empty in Go (Host lives in req.Host / req.URL.Host). Every PUT then signed
// "host:" (empty) while sending the real Host -> R2 answered HTTP 403.
func TestSignVerifiesAgainstWireHeaders(t *testing.T) {
	c := &r2Client{endpoint: "https://x.r2.cloudflarestorage.com", bucket: "b",
		region: "auto", access: "AK", secret: "SK"}
	body := []byte("fakejpegbytes")
	req, _ := http.NewRequest("PUT", c.url("history/a/N/2026-11-10T12-00-00.jpg"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
	when := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	c.sign(req, sha256Hex(body), when)

	// Independent re-verification from the wire's point of view.
	auth := req.Header.Get("Authorization")
	var scope, signedList, wantSig string
	for _, part := range strings.Split(auth, " ") {
		if strings.HasPrefix(part, "Credential=") {
			cred := strings.TrimSuffix(strings.TrimPrefix(part, "Credential="), ",")
			creds := strings.SplitN(cred, "/", 2)
			if creds[0] != "AK" {
				t.Fatalf("credential access key = %q, want AK", creds[0])
			}
			scope = creds[1]
		} else if strings.HasPrefix(part, "SignedHeaders=") {
			signedList = strings.TrimSuffix(strings.TrimPrefix(part, "SignedHeaders="), ",")
		} else if strings.HasPrefix(part, "Signature=") {
			wantSig = part[len("Signature="):]
		}
	}
	if scope == "" || signedList == "" || wantSig == "" {
		t.Fatalf("unparsable Authorization header: %q", auth)
	}
	// Effective headers as the server sees them: Host comes from the URL,
	// not from req.Header (Go keeps it out of the map).
	effective := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256"),
		"x-amz-date":           req.Header.Get("X-Amz-Date"),
		"content-type":         req.Header.Get("Content-Type"),
		"cache-control":        req.Header.Get("Cache-Control"),
	}
	var ch strings.Builder
	for _, h := range strings.Split(signedList, ";") {
		v, ok := effective[h]
		if !ok {
			t.Fatalf("signed header %q is not sent on the wire", h)
		}
		ch.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	uri := req.URL.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	payloadHash := sha256Hex(body)
	canonical := "PUT\n" + uri + "\n\n" + ch.String() + "\n" + signedList + "\n" + payloadHash
	amzDate := when.UTC().Format("20060102T150405Z")
	dateStamp := when.UTC().Format("20060102")
	credScope := dateStamp + "/" + c.region + "/s3/aws4_request"
	if signedList != "cache-control;content-type;host;x-amz-content-sha256;x-amz-date" {
		t.Fatalf("SignedHeaders not sorted: %q", signedList)
	}
	if scope != credScope {
		t.Fatalf("credential scope = %q, want %q", scope, credScope)
	}
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credScope + "\n" + sha256Hex([]byte(canonical))
	gotSig := hexHMAC(deriveSigningKey(c.secret, dateStamp, c.region, "s3"), sts)
	if gotSig != wantSig {
		t.Fatalf("signature does not verify against wire headers (signed an unsent value)")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
