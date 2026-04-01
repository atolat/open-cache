// loadgen simulates a Bazel build against a remote cache server.
//
// It generates the same HTTP request pattern Bazel makes:
//   1. GET /ac/{hash}  — check if action result is cached
//   2. On hit:  GET /cas/{hash}  — download cached outputs
//   3. On miss: PUT /cas/{hash}  — upload build outputs
//                PUT /ac/{hash}  — upload action result
//
// Usage:
//   go run ./cmd/loadgen/ -url http://localhost:8080 -actions 70000 -hits 30000 -jobs 64
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"log"
	mathrand "math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds load generator parameters.
type Config struct {
	URL        string // cache server URL
	Actions    int    // total number of build actions
	Hits       int    // how many actions are cache hits
	Jobs       int    // max concurrent requests (like Bazel --jobs)
	ACSize     int    // size of AC entries in bytes
	CASMinSize int    // minimum CAS blob size
	CASMaxSize int    // maximum CAS blob size
}

// Stats tracks request counts and latencies.
type Stats struct {
	acGetCount    atomic.Int64
	acGetHit      atomic.Int64
	acGetMiss     atomic.Int64
	casPutCount   atomic.Int64
	acPutCount    atomic.Int64
	casGetCount   atomic.Int64
	errors        atomic.Int64
	totalRequests atomic.Int64

	mu        sync.Mutex
	latencies []time.Duration // all request latencies
}

func main() {
	cfg := Config{}
	flag.StringVar(&cfg.URL, "url", "http://localhost:8080", "cache server URL")
	flag.IntVar(&cfg.Actions, "actions", 70000, "total build actions")
	flag.IntVar(&cfg.Hits, "hits", 30000, "number of cache hits")
	flag.IntVar(&cfg.Jobs, "jobs", 64, "concurrent requests")
	flag.IntVar(&cfg.ACSize, "ac-size", 500, "AC entry size in bytes")
	flag.IntVar(&cfg.CASMinSize, "cas-min", 1024, "minimum CAS blob size")
	flag.IntVar(&cfg.CASMaxSize, "cas-max", 1024*1024, "maximum CAS blob size")
	skipPopulate := flag.Bool("skip-populate", false, "skip cache population (assumes cache is warm)")
	flag.Parse()

	if cfg.Hits > cfg.Actions {
		log.Fatal("hits cannot exceed actions")
	}

	log.Printf("simulating build: %d actions, %d hits, %d concurrent",
		cfg.Actions, cfg.Hits, cfg.Jobs)

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        cfg.Jobs * 2,
			MaxIdleConnsPerHost: cfg.Jobs * 2,
			IdleConnTimeout:     30 * time.Second,
		},
		Timeout: 30 * time.Second,
	}

	stats := &Stats{}

	// Phase 1: Populate cache for the "hit" actions.
	if !*skipPopulate {
		log.Printf("phase 1: populating cache with %d entries...", cfg.Hits)
		populateStart := time.Now()
		populateCache(client, cfg, stats)
		log.Printf("phase 1 done in %s", time.Since(populateStart).Round(time.Millisecond))
	} else {
		log.Printf("phase 1: skipped (assuming warm cache)")
	}

	// Reset stats for the actual build simulation.
	*stats = Stats{}

	// Phase 2: Simulate a build.
	// First N actions are hits (AC exists), rest are misses.
	log.Printf("phase 2: simulating build...")
	buildStart := time.Now()
	simulateBuild(client, cfg, stats)
	buildDuration := time.Since(buildStart)

	// Print results.
	printResults(stats, buildDuration, cfg)
}

// populateCache uploads AC + CAS entries for the actions that should be cache hits.
func populateCache(client *http.Client, cfg Config, stats *Stats) {
	sem := make(chan struct{}, cfg.Jobs)
	var wg sync.WaitGroup

	for i := 0; i < cfg.Hits; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(actionID int) {
			defer wg.Done()
			defer func() { <-sem }()

			acHash := actionHash(actionID)
			casHash := contentHash(actionID)
			casData := randomBlob(cfg.CASMinSize, cfg.CASMaxSize)
			acData := fakeActionResult(casHash, len(casData))

			// PUT CAS first (same order as Bazel).
			doPut(client, cfg.URL+"/cas/"+casHash, casData, stats)
			// PUT AC.
			doPut(client, cfg.URL+"/ac/"+acHash, acData, stats)
		}(i)
	}
	wg.Wait()
}

// simulateBuild simulates all actions in a build.
func simulateBuild(client *http.Client, cfg Config, stats *Stats) {
	sem := make(chan struct{}, cfg.Jobs)
	var wg sync.WaitGroup

	for i := 0; i < cfg.Actions; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(actionID int) {
			defer wg.Done()
			defer func() { <-sem }()

			acHash := actionHash(actionID)
			isHit := actionID < cfg.Hits

			// Step 1: Check AC.
			status := doGet(client, cfg.URL+"/ac/"+acHash, stats)
			stats.acGetCount.Add(1)

			if status == 200 && isHit {
				// Cache hit — download CAS blob.
				stats.acGetHit.Add(1)
				casHash := contentHash(actionID)
				doGet(client, cfg.URL+"/cas/"+casHash, stats)
				stats.casGetCount.Add(1)
			} else {
				// Cache miss — "build" then upload.
				stats.acGetMiss.Add(1)
				casHash := contentHash(actionID)
				casData := randomBlob(cfg.CASMinSize, cfg.CASMaxSize)
				acData := fakeActionResult(casHash, len(casData))

				doPut(client, cfg.URL+"/cas/"+casHash, casData, stats)
				stats.casPutCount.Add(1)
				doPut(client, cfg.URL+"/ac/"+acHash, acData, stats)
				stats.acPutCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
}

// doGet performs a GET request and records latency.
func doGet(client *http.Client, url string, stats *Stats) int {
	start := time.Now()
	resp, err := client.Get(url)
	elapsed := time.Since(start)

	stats.totalRequests.Add(1)
	stats.mu.Lock()
	stats.latencies = append(stats.latencies, elapsed)
	stats.mu.Unlock()

	if err != nil {
		stats.errors.Add(1)
		return 0
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// doPut performs a PUT request and records latency.
func doPut(client *http.Client, url string, data []byte, stats *Stats) {
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	resp, err := client.Do(req)
	elapsed := time.Since(start)

	stats.totalRequests.Add(1)
	stats.mu.Lock()
	stats.latencies = append(stats.latencies, elapsed)
	stats.mu.Unlock()

	if err != nil {
		stats.errors.Add(1)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// printResults outputs the benchmark summary.
func printResults(stats *Stats, duration time.Duration, cfg Config) {
	total := stats.totalRequests.Load()
	rps := float64(total) / duration.Seconds()

	// Sort latencies for percentile calculation.
	stats.mu.Lock()
	sort.Slice(stats.latencies, func(i, j int) bool {
		return stats.latencies[i] < stats.latencies[j]
	})
	lats := stats.latencies
	stats.mu.Unlock()

	fmt.Println()
	fmt.Println("=== Load Test Results ===")
	fmt.Println()
	fmt.Printf("  Actions:        %d (%d hits, %d misses)\n",
		cfg.Actions, stats.acGetHit.Load(), stats.acGetMiss.Load())
	fmt.Printf("  Concurrency:    %d\n", cfg.Jobs)
	fmt.Printf("  Total requests: %d\n", total)
	fmt.Printf("  Errors:         %d\n", stats.errors.Load())
	fmt.Printf("  Duration:       %s\n", duration.Round(time.Millisecond))
	fmt.Printf("  Throughput:     %.0f req/s\n", rps)
	fmt.Println()
	fmt.Println("  Request breakdown:")
	fmt.Printf("    AC GET:       %d (%d hit, %d miss)\n",
		stats.acGetCount.Load(), stats.acGetHit.Load(), stats.acGetMiss.Load())
	fmt.Printf("    CAS GET:      %d\n", stats.casGetCount.Load())
	fmt.Printf("    CAS PUT:      %d\n", stats.casPutCount.Load())
	fmt.Printf("    AC PUT:       %d\n", stats.acPutCount.Load())
	fmt.Println()

	if len(lats) > 0 {
		fmt.Println("  Latency:")
		fmt.Printf("    P50:          %s\n", percentile(lats, 0.50))
		fmt.Printf("    P90:          %s\n", percentile(lats, 0.90))
		fmt.Printf("    P99:          %s\n", percentile(lats, 0.99))
		fmt.Printf("    Max:          %s\n", lats[len(lats)-1])
	}
	fmt.Println()
}

// percentile returns the value at the given percentile (0.0-1.0).
func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// actionHash generates a deterministic AC hash for an action ID.
func actionHash(id int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("action-%d", id)))
	return fmt.Sprintf("%x", h)
}

// contentHash generates a deterministic CAS hash for an action's output.
func contentHash(id int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("content-%d", id)))
	return fmt.Sprintf("%x", h)
}

// fakeActionResult creates a realistic-sized AC entry.
func fakeActionResult(casHash string, casSize int) []byte {
	return []byte(fmt.Sprintf(`{"output":"%s","size":%d}`, casHash, casSize))
}

// randomBlob generates a random blob with size between min and max.
func randomBlob(min, max int) []byte {
	size := min + mathrand.IntN(max-min+1)
	data := make([]byte, size)
	rand.Read(data)
	return data
}
