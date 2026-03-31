// Package tiered provides a two-tier cache: L1 (memory) + L2 (S3).
//
// Write path: write to both L1 and L2.
// Read path: check L1 first. On miss, fetch from L2 and populate L1.
//
// L1 is any cache.Store (in-memory, Valkey, etc.).
// L2 is S3 (always present, durable).
package tiered

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/atolat/open-cache/internal/cache"
)

// Store is a two-tier cache with L1 (memory) in front of L2 (S3).
type Store struct {
	l1     cache.Store
	s3     *s3.Client
	bucket string

	// maxL1BlobSize is the maximum blob size to cache in L1.
	// Blobs larger than this go to S3 only.
	maxL1BlobSize int64
}

// New creates a tiered store.
//
// l1 is the in-memory cache. s3Client is the S3 backend.
// maxL1BlobSize controls which blobs are cached in L1 — blobs
// larger than this threshold skip L1 and go directly to S3.
func New(l1 cache.Store, s3Client *s3.Client, bucket string, maxL1BlobSize int64) *Store {
	return &Store{
		l1:            l1,
		s3:            s3Client,
		bucket:        bucket,
		maxL1BlobSize: maxL1BlobSize,
	}
}

// Get reads from L1 first. On miss, fetches from S3 and populates L1.
func (t *Store) Get(ctx context.Context, key string) ([]byte, error) {
	// Check L1.
	if data, ok := t.l1.Get(key); ok {
		return data, nil
	}

	// L1 miss — fetch from S3.
	out, err := t.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &t.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}

	// Populate L1 if eligible (AC entries always, CAS under size limit).
	if t.ShouldCacheInL1(key, int64(len(data))) {
		t.l1.Put(key, data)
	}

	return data, nil
}

// Put writes to both L1 and S3.
func (t *Store) Put(ctx context.Context, key string, data []byte) error {
	// Write to L1 if eligible (AC entries always, CAS under size limit).
	if t.ShouldCacheInL1(key, int64(len(data))) {
		t.l1.Put(key, data)
	}

	// Always write to S3.
	contentLen := int64(len(data))
	_, err := t.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &t.bucket,
		Key:           &key,
		Body:          bytes.NewReader(data),
		ContentLength: &contentLen,
	})
	if err != nil {
		log.Printf("PUT %s to S3 failed: %v", key, err)
		return err
	}

	return nil
}

// Has checks L1 first, then S3.
func (t *Store) Has(ctx context.Context, key string) bool {
	// Check L1.
	if t.l1.Has(key) {
		return true
	}

	// Check S3.
	_, err := t.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &t.bucket,
		Key:    &key,
	})
	return err == nil
}

// ContentLength returns the size of an object, checking L1 then S3.
func (t *Store) ContentLength(ctx context.Context, key string) (int64, bool) {
	// Check L1.
	if data, ok := t.l1.Get(key); ok {
		return int64(len(data)), true
	}

	// Check S3.
	out, err := t.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &t.bucket,
		Key:    &key,
	})
	if err != nil {
		return 0, false
	}

	if out.ContentLength != nil {
		return *out.ContentLength, true
	}
	return 0, true
}

// IsACKey returns true if the key is an Action Cache entry.
// AC keys start with "ac/". Used to decide caching strategy —
// AC entries are always cached in L1 regardless of size.
func IsACKey(key string) bool {
	return strings.HasPrefix(key, "ac/")
}

// ShouldCacheInL1 decides whether a blob should be stored in L1.
// AC entries: always (they're small and always hot).
// CAS entries: only if under the size threshold.
func (t *Store) ShouldCacheInL1(key string, size int64) bool {
	if IsACKey(key) {
		return true
	}
	return size <= t.maxL1BlobSize
}

// L1 returns the underlying L1 store for stats/debugging.
func (t *Store) L1() cache.Store {
	return t.l1
}
