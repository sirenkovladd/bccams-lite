package main

import (
	"net/http"
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
