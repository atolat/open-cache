package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Server handles Bazel remote cache HTTP requests and proxies them to S3.
type Server struct {
	s3       *s3.Client
	uploader *manager.Uploader
	bucket   string
}

// New creates a Server connected to the given S3 bucket.
func New(bucket, region string) (*Server, error) {
	return newServer(bucket, region, nil)
}

// NewWithEndpoint creates a Server pointing at a custom S3-compatible endpoint.
// Used for testing against a fake S3 or MinIO.
func NewWithEndpoint(bucket, region, endpoint string) (*Server, error) {
	return newServer(bucket, region, &endpoint)
}

func newServer(bucket, region string, endpoint *string) (*Server, error) {
	configOpts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	// When using a custom endpoint (testing/MinIO), use static dummy
	// credentials so the SDK doesn't try to reach EC2 IMDS.
	if endpoint != nil {
		configOpts = append(configOpts,
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		)
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), configOpts...)
	if err != nil {
		return nil, err
	}

	opts := func(o *s3.Options) {
		if endpoint != nil {
			o.BaseEndpoint = endpoint
			o.UsePathStyle = true
		}
		// Disable automatic checksum computation — avoids needing
		// seekable request bodies for streaming uploads.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}

	client := s3.NewFromConfig(cfg, opts)

	// The upload manager handles multipart uploads for large objects.
	// It reads from the body in chunks (default 5MB) and uploads parts
	// concurrently — no need to buffer the entire object in memory.
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = 64 * 1024 * 1024 // 64MB parts
		u.Concurrency = 4
	})

	return &Server{
		s3:       client,
		uploader: uploader,
		bucket:   bucket,
	}, nil
}

// ServeHTTP routes incoming requests by HTTP method.
// Go's http package calls this for every request, each in its own goroutine.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/")

	if key == "healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r, key)
	case http.MethodPut:
		s.handlePut(w, r, key)
	case http.MethodHead:
		s.handleHead(w, r, key)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGet streams an object from S3 to the client.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, key string) {
	out, err := s.s3.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer out.Body.Close()

	if out.ContentLength != nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", *out.ContentLength))
	}
	w.WriteHeader(http.StatusOK)
	io.Copy(w, out.Body)
}

// handlePut streams the request body to S3.
// Uses the upload manager which handles multipart for large objects
// and does not buffer the entire body in memory.
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	_, err := s.uploader.Upload(r.Context(), &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   r.Body,
	})
	if err != nil {
		log.Printf("PUT %s failed: %v", key, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleHead checks if an object exists in S3.
func (s *Server) handleHead(w http.ResponseWriter, r *http.Request, key string) {
	out, err := s.s3.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if out.ContentLength != nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", *out.ContentLength))
	}
	w.WriteHeader(http.StatusOK)
}
