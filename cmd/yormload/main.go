// Command yormload runs bots against a Yorm server and reports latency and
// consistency: how long a command takes to reach every client, whether any
// event was lost or out of order, and whether every client's state matches
// what the server says it should see.
//
//	yormload -url http://localhost:8080 -sessions 500 -duration 2m
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SpandanDhru/yorm/internal/bot"
)

type result struct {
	Sessions   int           `json:"sessions"`
	Clients    int           `json:"clients"`
	Interval   string        `json:"interval"`
	Duration   string        `json:"duration"`
	Commands   float64       `json:"commands_per_sec"`
	Diverged   int           `json:"diverged"`
	Unsettled  string        `json:"unsettled,omitempty"`
	ServerCPU  float64       `json:"server_cpu_cores"`
	ServerRSS  float64       `json:"server_rss_mb"`
	TargetP50  float64       `json:"target_p50_ms"`
	TargetP99  float64       `json:"target_p99_ms"`
	Pass       bool          `json:"pass"`
	Report     bot.Report    `json:"report"`
	Setup      time.Duration `json:"setup_ns"`
	SetupFails int           `json:"setup_failures"`
}

func main() {
	url := flag.String("url", "http://localhost:8080", "server to test")
	sessions := flag.Int("sessions", 10, "sessions (tables) to run")
	players := flag.Int("players", 5, "players per table, plus a DM")
	interval := flag.Duration("interval", 3*time.Second, "average time between one bot's actions")
	duration := flag.Duration("duration", time.Minute, "how long to play once every table is set up")
	ramp := flag.Duration("ramp", 20*time.Second, "time over which to open the tables")
	out := flag.String("json", "", "write the result as JSON here")
	p50 := flag.Float64("p50", 10, "target p50 command-to-broadcast latency, ms")
	p99 := flag.Float64("p99", 50, "target p99 command-to-broadcast latency, ms")
	flag.Parse()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stats := bot.NewStats()
	res := result{Sessions: *sessions, Clients: *sessions * (*players + 1), Interval: interval.String(), Duration: duration.String(), TargetP50: *p50, TargetP99: *p99}

	fmt.Printf("opening %d tables (%d clients) over %v...\n", *sessions, res.Clients, *ramp)
	start := time.Now()
	var (
		mu      sync.Mutex
		tables  []*bot.Table
		wg      sync.WaitGroup
		failed  int
		pending = make(chan struct{}, 32) // at most 32 tables setting up at once
	)
	for i := range *sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(int64(*ramp) * int64(i) / int64(max(1, *sessions))))
			pending <- struct{}{}
			tbl, err := bot.NewTable(ctx, *url, *players, stats)
			<-pending
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				fmt.Fprintln(os.Stderr, "setup:", err)
				return
			}
			tables = append(tables, tbl)
		}()
	}
	wg.Wait()
	res.Setup, res.SetupFails = time.Since(start), failed
	fmt.Printf("%d tables ready in %v (%d failed); playing for %v\n", len(tables), res.Setup.Round(time.Millisecond), failed, *duration)

	// Only what happens during play counts toward latency and throughput.
	stats.ResetLatency()
	cpu0, _ := serverMetric(*url, "process_cpu_seconds_total")
	commands0 := stats.Commands.Load()
	play, done := context.WithTimeout(ctx, *duration)
	for _, t := range tables {
		go t.Play(play, *interval)
	}
	<-play.Done()
	done()
	cpu1, _ := serverMetric(*url, "process_cpu_seconds_total")
	rss, _ := serverMetric(*url, "process_resident_memory_bytes")
	res.Commands = float64(stats.Commands.Load()-commands0) / duration.Seconds()
	res.ServerCPU = (cpu1 - cpu0) / duration.Seconds()
	res.ServerRSS = rss / (1 << 20)

	var clients []*bot.Client
	for _, t := range tables {
		clients = append(clients, t.Clients()...)
	}
	fmt.Println("settling and comparing every client's state with the server...")
	settle, cancel := context.WithTimeout(ctx, 2*time.Minute)
	if err := bot.Settle(settle, clients); err != nil {
		res.Unsettled = err.Error()
	}
	res.Diverged, _ = bot.Diverged(settle, clients)
	cancel()
	res.Report = stats.Report()
	r := res.Report
	res.Pass = r.P50 <= *p50 && r.P99 <= *p99 && r.Gaps == 0 && r.Errors == 0 && res.Diverged == 0 && res.Unsettled == "" && failed == 0

	fmt.Print(markdown(res, runtime.NumCPU()))
	if *out != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(*out, b, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	if !res.Pass {
		os.Exit(1)
	}
}

func markdown(r result, cpus int) string {
	pass := map[bool]string{true: "✅", false: "❌"}
	var b strings.Builder
	fmt.Fprintf(&b, "\n| Metric | Result | Target |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Sessions × clients | %d × %d = %d WebSockets | 500 × 6 |\n", r.Sessions, r.Clients/max(1, r.Sessions), r.Clients)
	fmt.Fprintf(&b, "| Commands | %.0f/s (one per bot every ~%s) | |\n", r.Commands, r.Interval)
	fmt.Fprintf(&b, "| Events delivered | %d (%d timed) | |\n", r.Report.Events, r.Report.Deliveries)
	fmt.Fprintf(&b, "| Command → broadcast p50 | %.2f ms | ≤ %.0f ms %s |\n", r.Report.P50, r.TargetP50, pass[r.Report.P50 <= r.TargetP50])
	fmt.Fprintf(&b, "| Command → broadcast p95 | %.2f ms | |\n", r.Report.P95)
	fmt.Fprintf(&b, "| Command → broadcast p99 | %.2f ms | ≤ %.0f ms %s |\n", r.Report.P99, r.TargetP99, pass[r.Report.P99 <= r.TargetP99])
	fmt.Fprintf(&b, "| Max | %.2f ms | |\n", r.Report.Max)
	fmt.Fprintf(&b, "| Lost or out-of-order events | %d | 0 %s |\n", r.Report.Gaps, pass[r.Report.Gaps == 0])
	fmt.Fprintf(&b, "| Clients whose state diverged | %d | 0 %s |\n", r.Diverged, pass[r.Diverged == 0])
	if r.Unsettled != "" {
		fmt.Fprintf(&b, "| Settled after the run | no: %s (so divergence may just be events still arriving) | ❌ |\n", r.Unsettled)
	}
	fmt.Fprintf(&b, "| Errors, disconnects | %d, %d | |\n", r.Report.Errors, r.Report.Disconnects)
	fmt.Fprintf(&b, "| Server CPU, memory | %.2f cores, %.0f MB | |\n", r.ServerCPU, r.ServerRSS)
	fmt.Fprintf(&b, "\nLoad generator on %d CPUs. Setup took %v.\n", cpus, r.Setup.Round(time.Second))
	return b.String()
}

// serverMetric reads one unlabeled value from the server's /metrics.
func serverMetric(url, name string) (float64, error) {
	resp, err := http.Get(url + "/metrics")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), name+" "); ok {
			return strconv.ParseFloat(v, 64)
		}
	}
	return 0, fmt.Errorf("%s not found", name)
}
