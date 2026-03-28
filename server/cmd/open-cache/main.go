package main

import (
	"log"
	"net/http"
	"os"

	"github.com/atolat/open-cache/internal/server"
)

func main() {
	// Read config from environment variables.
	bucket := envOrDefault("S3_BUCKET", "open-cache-bazel")
	region := envOrDefault("S3_REGION", "us-east-1")
	addr := envOrDefault("LISTEN_ADDR", ":8080")

	// Create the cache server.
	srv, err := server.New(bucket, region)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	log.Printf("listening on %s (bucket=%s, region=%s)", addr, bucket, region)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// envOrDefault reads an environment variable, returning a default if unset.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
