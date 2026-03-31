package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/atolat/open-cache/internal/server"
)

func main() {
	cfg := server.Config{
		Bucket:        envOrDefault("S3_BUCKET", "open-cache-bazel"),
		Region:        envOrDefault("S3_REGION", "us-east-1"),
		L1MaxBytes:    envOrDefaultInt64("L1_MAX_BYTES", 4*1024*1024*1024), // 4 GiB
		L1MaxBlobSize: envOrDefaultInt64("L1_MAX_BLOB_SIZE", 1*1024*1024),  // 1 MiB
	}
	addr := envOrDefault("LISTEN_ADDR", ":8080")

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	log.Printf("listening on %s (bucket=%s, l1=%d MB, max_blob=%d KB)",
		addr, cfg.Bucket, cfg.L1MaxBytes/(1024*1024), cfg.L1MaxBlobSize/1024)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return n
		}
	}
	return fallback
}
