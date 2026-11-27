package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// The backend confirms a versioned upload by checking the stored S3 object's Content-Type against
// exactly application/java-archive, and S3 stores whatever the PUT sent. Without the header the
// object is stored as binary/octet-stream and confirm fails with "Resource not loaded successfully
// or is not of type application/java-archive".
func TestPutObjectContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        string
	}{
		{name: "jar upload sends application/java-archive", contentType: JarContentType, want: "application/java-archive"},
		{name: "standalone upload sends none", contentType: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotType, gotAuth string
			var gotBody []byte
			s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotType = r.Header.Get("Content-Type")
				gotAuth = r.Header.Get("Authorization")
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer s3.Close()

			c, err := New(context.Background(), Config{AccessToken: "tok"})
			if err != nil {
				t.Fatal(err)
			}
			p := &client.PresignedUrl{Url: s3.URL + "/bucket/key", Method: http.MethodPut, ResourceKey: "key"}
			if err := c.putObject(context.Background(), p, []byte("PK\x03\x04"), tt.contentType); err != nil {
				t.Fatalf("putObject: %v", err)
			}

			if gotType != tt.want {
				t.Errorf("Content-Type = %q, want %q", gotType, tt.want)
			}
			if gotAuth != "" {
				t.Errorf("Authorization = %q, want none: a presigned URL carries its own signature", gotAuth)
			}
			if string(gotBody) != "PK\x03\x04" {
				t.Errorf("body = %q", gotBody)
			}
		})
	}
}
