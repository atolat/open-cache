package server_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atolat/open-cache/internal/server"
)

// newTestServer creates a server pointing at a fake S3 backend.
// We spin up a local HTTP server that pretends to be S3, and configure
// the real server to talk to it instead of AWS.
func newTestServer(t *testing.T) (*server.Server, *fakeS3) {
	t.Helper()
	fake := newFakeS3()
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)

	srv, err := server.NewWithEndpoint("test-bucket", "us-east-1", ts.URL)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	return srv, fake
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestGetMiss(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/cas/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET miss = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestPutThenGet(t *testing.T) {
	srv, _ := newTestServer(t)
	body := "hello cache"

	// PUT
	putReq := httptest.NewRequest(http.MethodPut, "/cas/abc123", strings.NewReader(body))
	putReq.ContentLength = int64(len(body))
	putRec := httptest.NewRecorder()
	srv.ServeHTTP(putRec, putReq)

	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want %d", putRec.Code, http.StatusOK)
	}

	// GET
	getReq := httptest.NewRequest(http.MethodGet, "/cas/abc123", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want %d", getRec.Code, http.StatusOK)
	}
	if got := getRec.Body.String(); got != body {
		t.Errorf("GET body = %q, want %q", got, body)
	}
}

func TestHead(t *testing.T) {
	srv, _ := newTestServer(t)
	body := "test data"

	// PUT first
	putReq := httptest.NewRequest(http.MethodPut, "/ac/def456", strings.NewReader(body))
	putReq.ContentLength = int64(len(body))
	putRec := httptest.NewRecorder()
	srv.ServeHTTP(putRec, putReq)

	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want %d", putRec.Code, http.StatusOK)
	}

	// HEAD hit
	headReq := httptest.NewRequest(http.MethodHead, "/ac/def456", nil)
	headRec := httptest.NewRecorder()
	srv.ServeHTTP(headRec, headReq)

	if headRec.Code != http.StatusOK {
		t.Errorf("HEAD hit = %d, want %d", headRec.Code, http.StatusOK)
	}

	// HEAD miss
	headReq2 := httptest.NewRequest(http.MethodHead, "/ac/missing", nil)
	headRec2 := httptest.NewRecorder()
	srv.ServeHTTP(headRec2, headReq2)

	if headRec2.Code != http.StatusNotFound {
		t.Errorf("HEAD miss = %d, want %d", headRec2.Code, http.StatusNotFound)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/cas/abc123", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestACAndCASNamespaces(t *testing.T) {
	srv, _ := newTestServer(t)

	// PUT to /ac/key1
	putAC := httptest.NewRequest(http.MethodPut, "/ac/key1", strings.NewReader("ac-data"))
	putAC.ContentLength = 7
	srv.ServeHTTP(httptest.NewRecorder(), putAC)

	// PUT to /cas/key1 (same key name, different namespace)
	putCAS := httptest.NewRequest(http.MethodPut, "/cas/key1", strings.NewReader("cas-data"))
	putCAS.ContentLength = 8
	srv.ServeHTTP(httptest.NewRecorder(), putCAS)

	// GET /ac/key1
	getAC := httptest.NewRequest(http.MethodGet, "/ac/key1", nil)
	acRec := httptest.NewRecorder()
	srv.ServeHTTP(acRec, getAC)

	if got := acRec.Body.String(); got != "ac-data" {
		t.Errorf("GET /ac/key1 = %q, want %q", got, "ac-data")
	}

	// GET /cas/key1
	getCAS := httptest.NewRequest(http.MethodGet, "/cas/key1", nil)
	casRec := httptest.NewRecorder()
	srv.ServeHTTP(casRec, getCAS)

	if got := casRec.Body.String(); got != "cas-data" {
		t.Errorf("GET /cas/key1 = %q, want %q", got, "cas-data")
	}
}

// --- Fake S3 backend ---

// fakeS3 is a minimal in-memory S3 implementation for testing.
// It handles GetObject, PutObject, and HeadObject.
type fakeS3 struct {
	objects map[string][]byte
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte)}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// S3 path format: /{bucket}/{key}
	// Strip the bucket name prefix to get the key.
	parts := strings.SplitN(r.URL.Path, "/", 3)
	if len(parts) < 3 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key := parts[2]

	switch r.Method {
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code></Error>`))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Write(data)

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.objects[key] = data
		w.WriteHeader(http.StatusOK)

	case http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
