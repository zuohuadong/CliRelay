package video

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestS3ObjectStoreUploadsAndPresignsPathStyleObject(t *testing.T) {
	var gotPath string
	var gotBody string
	var gotAuthorization string
	var gotPayloadHash string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotPayloadHash = r.Header.Get("X-Amz-Content-Sha256")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	store, err := NewS3ObjectStore(S3Config{
		Endpoint:        server.URL,
		Region:          "auto",
		Bucket:          "video-bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
		PathStyle:       true,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewS3ObjectStore() error = %v", err)
	}

	if err = store.Put(context.Background(), "generated/video_public_123.mp4", strings.NewReader("video-bytes"), int64(len("video-bytes")), "video/mp4"); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if gotPath != "/video-bucket/generated/video_public_123.mp4" {
		t.Fatalf("PUT path = %q", gotPath)
	}
	if gotBody != "video-bytes" {
		t.Fatalf("PUT body = %q", gotBody)
	}
	if !strings.Contains(gotAuthorization, "Credential=access-key/") || !strings.Contains(gotAuthorization, "/s3/aws4_request") {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if gotPayloadHash != sha256Hex([]byte("video-bytes")) || gotPayloadHash == s3UnsignedPayload {
		t.Fatalf("X-Amz-Content-Sha256 = %q", gotPayloadHash)
	}

	signed, err := store.SignedURL("generated/video_public_123.mp4", 10*time.Minute)
	if err != nil {
		t.Fatalf("SignedURL() error = %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	if parsed.Path != "/video-bucket/generated/video_public_123.mp4" {
		t.Fatalf("signed path = %q", parsed.Path)
	}
	for _, key := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature"} {
		if parsed.Query().Get(key) == "" {
			t.Fatalf("signed URL missing %s: %s", key, signed)
		}
	}
}

func TestS3ObjectStoreRejectsInsecureRemoteAndCredentialedEndpoints(t *testing.T) {
	base := S3Config{Region: "auto", Bucket: "videos", AccessKeyID: "access-key", SecretAccessKey: "secret-key"}
	base.Endpoint = "http://storage.example.com"
	if _, err := NewS3ObjectStore(base, nil); err == nil {
		t.Fatal("NewS3ObjectStore() error = nil, want remote HTTP rejection")
	}
	base.Endpoint = "https://user:secret@storage.example.com"
	if _, err := NewS3ObjectStore(base, nil); err == nil {
		t.Fatal("NewS3ObjectStore() error = nil, want endpoint userinfo rejection")
	}
}

func TestS3ObjectStoreEscapesReservedObjectPathBytes(t *testing.T) {
	store, err := NewS3ObjectStore(S3Config{
		Endpoint:        "https://storage.example.com",
		Region:          "auto",
		Bucket:          "videos",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
		PathStyle:       true,
	}, nil)
	if err != nil {
		t.Fatalf("NewS3ObjectStore() error = %v", err)
	}
	objectURL, err := store.objectURL("prefix/a+b:c@d.mp4")
	if err != nil {
		t.Fatalf("objectURL() error = %v", err)
	}
	if got := canonicalS3Path(objectURL); got != "/videos/prefix/a%2Bb%3Ac%40d.mp4" {
		t.Fatalf("canonical path = %q", got)
	}
}
