//go:build integration

package bot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/bot"
	"github.com/SpandanDhru/yorm/internal/db/dbtest"
)

// server is the real yormd binary, run as a separate process so the test
// can kill it without warning.
type server struct {
	t    *testing.T
	bin  string
	env  []string
	addr string
	cmd  *exec.Cmd
	out  bytes.Buffer
}

func (s *server) start() {
	s.t.Helper()
	s.cmd = exec.Command(s.bin) //nolint:gosec // the yormd this test just built
	s.cmd.Env = append(os.Environ(), s.env...)
	s.cmd.Stdout, s.cmd.Stderr = &s.out, &s.out
	if err := s.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get("http://" + s.addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("yormd didn't start: %v\n%s", err, s.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// kill stops the server with SIGKILL: no shutdown, no final snapshots.
func (s *server) kill() {
	s.t.Helper()
	if err := s.cmd.Process.Kill(); err != nil {
		s.t.Fatal(err)
	}
	_ = s.cmd.Wait()
}

// TestChaos kills the server with SIGKILL in the middle of three games and
// restarts it. The bots reconnect on their own and keep playing. Nothing
// the server acknowledged may be lost, nothing may be applied twice, and
// every bot must end with exactly the state the server says it should see.
func TestChaos(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := filepath.Join(dir, "yormd")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/yormd").CombinedOutput(); err != nil { //nolint:gosec // a fixed build into a temp dir
		t.Fatalf("build yormd: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	srv := &server{t: t, bin: bin, addr: addr, env: []string{
		"DATABASE_URL=" + dbtest.StartPostgres(t),
		"YORM_TOKEN_SECRET=" + strings.Repeat("s", 32),
		"YORM_ADDR=" + addr,
		"YORM_UPLOAD_DIR=" + filepath.Join(dir, "uploads"),
	}}
	srv.start()
	t.Cleanup(func() {
		if srv.cmd.ProcessState == nil {
			srv.kill()
		}
	})
	base := "http://" + addr

	stats := bot.NewStats()
	run, stop := context.WithCancel(ctx)
	defer stop()
	var tables []*bot.Table
	var clients []*bot.Client
	play, done := context.WithTimeout(run, 8*time.Second)
	defer done()
	for range 3 {
		tbl, err := bot.NewTable(run, base, 5, stats)
		if err != nil {
			t.Fatal(err)
		}
		tables = append(tables, tbl)
		clients = append(clients, tbl.Clients()...)
		go tbl.Play(play, 50*time.Millisecond)
	}

	time.Sleep(3 * time.Second)
	before := stats.Report()
	srv.kill()
	t.Logf("killed yormd after %d commands answered", before.Acks+before.Rejects)
	time.Sleep(500 * time.Millisecond)
	srv.start()
	<-play.Done()

	settle, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := bot.Settle(settle, clients); err != nil {
		t.Fatalf("%v\nserver log:\n%s", err, lastLines(srv.out.String(), 20))
	}
	diverged, err := bot.Diverged(ctx, clients)
	if err != nil {
		t.Fatal(err)
	}
	r := stats.Report()
	t.Logf("%+v", r)
	if r.Reconnects < int64(len(clients)) {
		t.Fatalf("only %d reconnects for %d bots", r.Reconnects, len(clients))
	}
	if diverged != 0 {
		t.Fatalf("%d bots diverged from the server after the crash", diverged)
	}

	// Every acknowledged command is in the log, and no command's events
	// appear twice (a resend after the crash must not be applied again).
	acked := stats.Acked()
	found := 0
	for _, tbl := range tables {
		evs := history(t, base, tbl)
		bySeq := map[int64]string{}
		lastSeqOf := map[string]int64{}
		for _, ev := range evs {
			bySeq[ev.Seq] = ev.Cause
			if ev.Cause == "" {
				continue
			}
			if prev, ok := lastSeqOf[ev.Cause]; ok && prev != ev.Seq-1 {
				t.Fatalf("command %s was applied twice (seqs %d and %d)", ev.Cause, prev, ev.Seq)
			}
			lastSeqOf[ev.Cause] = ev.Seq
		}
		for id, seq := range acked {
			if cause, ok := bySeq[seq]; ok && cause == id {
				found++
			}
		}
	}
	accepted := 0
	for _, seq := range acked {
		if seq > 0 {
			accepted++
		}
	}
	if found != accepted {
		t.Fatalf("%d commands were acknowledged but only %d are in the log", accepted, found)
	}
	t.Logf("all %d acknowledged commands are in the log", found)
}

type event struct {
	Seq   int64  `json:"seq"`
	Cause string `json:"cause"`
}

func history(t *testing.T, base string, tbl *bot.Table) []event {
	t.Helper()
	var all []event
	after := int64(0)
	for {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/sessions/%s/events?after=%d&limit=1000", base, tbl.Session, after), nil)
		req.Header.Set("Authorization", "Bearer "+tbl.DMToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Events []event `json:"events"`
			Next   *int64  `json:"next"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Events...)
		if page.Next == nil {
			return all
		}
		after = *page.Next
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
