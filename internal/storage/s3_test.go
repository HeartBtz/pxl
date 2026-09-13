package storage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3UsesPrivateServerSideRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture-bucket/ab/image.png" {
			t.Error("unexpected object key")
		}
		if r.Header.Get("X-Amz-Acl") != "" {
			t.Error("unexpected ACL override")
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("unsigned S3 request")
		}
		if r.Method == "GET" {
			if r.Header.Get("Range") != "bytes=2-4" {
				t.Error("missing range")
			}
			w.Header().Set("Content-Length", "3")
			w.Header().Set("Content-Range", "bytes 2-4/6")
			w.WriteHeader(206)
			_, _ = w.Write([]byte("cde"))
			return
		}
		if r.Method == "PUT" {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	// Explicit static fixture credentials/client: never load AWS env/shared config.
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture", "fixture-only", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	b := &S3Backend{client: client, bucket: "fixture-bucket"}
	if _, err := b.Put(t.Context(), "ab/image.png", strings.NewReader("abcdef"), 6); err != nil {
		t.Fatal("fixture put failed")
	}
	rc, err := b.GetRange(t.Context(), "ab/image.png", 2, 3)
	if err != nil {
		t.Fatal("fixture range failed")
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil || string(data) != "cde" {
		t.Fatal("range bytes mismatch")
	}
	if err := b.Delete(t.Context(), "ab/image.png"); err != nil {
		t.Fatal("fixture delete failed")
	}
}
