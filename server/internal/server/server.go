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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/atolat/open-cache/internal/cache/evictor"
	"github.com/atolat/open-cache/internal/cache/memory"
	"github.com/atolat/open-cache/internal/cache/tiered"
)

// Server handles Bazel remote cache HTTP requests.
type Server struct {
	store *tiered.Store
}

// Config holds server configuration.
type Config struct {
	Bucket        string
	Region        string
	Endpoint      string // optional, for testing
	L1MaxBytes    int64  // max L1 cache size in bytes
	L1MaxBlobSize int64  // max blob size to cache in L1
}

// New creates a Server with a tiered cache (L1 memory + L2 S3).
func New(cfg Config) (*Server, error) {
	s3Client, err := newS3Client(cfg)
	if err != nil {
		return nil, err
	}

	// Create L1 in-memory cache with LRU eviction.
	l1 := memory.New(cfg.L1MaxBytes, evictor.NewLRU())

	// Create tiered store: L1 (memory) → L2 (S3).
	store := tiered.New(l1, s3Client, cfg.Bucket, cfg.L1MaxBlobSize)

	return &Server{store: store}, nil
}

// newS3Client creates an S3 client.
// If cfg.Endpoint is set, it points at a custom endpoint (for testing).
func newS3Client(cfg Config) (*s3.Client, error) {
	configOpts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
	}
	if cfg.Endpoint != "" {
		configOpts = append(configOpts,
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		)
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), configOpts...)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
			o.UsePathStyle = true
		}
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}), nil
}

// ServeHTTP routes incoming requests by HTTP method.
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

// handleGet reads from the tiered cache (L1 → S3).
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, key string) {
	data, err := s.store.Get(r.Context(), key)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// handlePut writes to the tiered cache (L1 + S3).
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("PUT %s read failed: %v", key, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := s.store.Put(r.Context(), key, data); err != nil {
		log.Printf("PUT %s failed: %v", key, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleHead checks existence in the tiered cache (L1 → S3).
func (s *Server) handleHead(w http.ResponseWriter, r *http.Request, key string) {
	size, ok := s.store.ContentLength(r.Context(), key)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(http.StatusOK)
}

// L1Stats returns the current L1 cache stats for debugging.
func (s *Server) L1Stats() (entries int, sizeBytes int64) {
	return s.store.L1().Len(), s.store.L1().Size()
}
