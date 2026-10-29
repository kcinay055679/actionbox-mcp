package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actionbox/internal/protocol"
)

func fakeBroker(t *testing.T, reply func(conn net.Conn, req protocol.Request)) (socketPath string, requests chan protocol.Request) {
	t.Helper()
	dir, err := os.MkdirTemp("", "abc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath = filepath.Join(dir, "b.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	requests = make(chan protocol.Request, 10)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var req protocol.Request
				if err := json.Unmarshal(line, &req); err != nil {
					return
				}
				requests <- req
				reply(conn, req)
			}()
		}
	}()
	return socketPath, requests
}

func respond(resp protocol.Response) func(net.Conn, protocol.Request) {
	return func(conn net.Conn, _ protocol.Request) {
		b, _ := json.Marshal(resp)
		conn.Write(append(b, '\n'))
	}
}

func TestCallSendsRequestAndReturnsResponse(t *testing.T) {
	// given
	sock, requests := fakeBroker(t, respond(protocol.Response{Job: &protocol.Job{ID: "j1", State: protocol.JobRunning}}))
	wd, _ := os.Getwd()

	// when
	resp, err := Call(context.Background(), sock, protocol.Request{Op: protocol.OpJobStatus, JobID: "j1"})

	// then
	if err != nil {
		t.Fatal(err)
	}
	if resp.Job == nil || resp.Job.ID != "j1" || resp.Job.State != protocol.JobRunning {
		t.Fatalf("resp = %+v", resp)
	}
	req := <-requests
	if req.Op != protocol.OpJobStatus || req.JobID != "j1" || req.Cwd != wd {
		t.Fatalf("req = %+v, want cwd %q", req, wd)
	}
}

func TestCallKeepsExplicitCwd(t *testing.T) {
	// given
	sock, requests := fakeBroker(t, respond(protocol.Response{}))

	// when
	_, err := Call(context.Background(), sock, protocol.Request{Op: protocol.OpListActions, Cwd: "/some/dir"})

	// then
	if err != nil {
		t.Fatal(err)
	}
	if req := <-requests; req.Cwd != "/some/dir" {
		t.Fatalf("cwd = %q", req.Cwd)
	}
}

func TestCallReturnsBrokerError(t *testing.T) {
	// given
	sock, _ := fakeBroker(t, respond(protocol.Response{Error: "untrusted: rollback"}))

	// when
	_, err := Call(context.Background(), sock, protocol.Request{Op: protocol.OpListActions})

	// then
	var brokerErr *BrokerError
	if !errors.As(err, &brokerErr) {
		t.Fatalf("err = %v (%T), want *BrokerError", err, err)
	}
	if brokerErr.Message != "untrusted: rollback" || err.Error() != "untrusted: rollback" {
		t.Fatalf("err = %q", err)
	}
}

func TestCallWithoutBrokerIsTransportError(t *testing.T) {
	// given
	sock := filepath.Join(t.TempDir(), "missing.sock")

	// when
	_, err := Call(context.Background(), sock, protocol.Request{Op: protocol.OpListActions})

	// then
	var brokerErr *BrokerError
	if err == nil || errors.As(err, &brokerErr) {
		t.Fatalf("err = %v, want transport error", err)
	}
}

func TestCallHonorsContextDeadline(t *testing.T) {
	// given
	sock, _ := fakeBroker(t, func(conn net.Conn, _ protocol.Request) { time.Sleep(5 * time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()

	// when
	_, err := Call(ctx, sock, protocol.Request{Op: protocol.OpListActions})

	// then
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v", elapsed)
	}
}

func TestCallHonorsContextCancel(t *testing.T) {
	// given
	sock, _ := fakeBroker(t, func(conn net.Conn, _ protocol.Request) { time.Sleep(5 * time.Second) })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	// when
	_, err := Call(ctx, sock, protocol.Request{Op: protocol.OpListActions})

	// then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestCallRejectsOversizedResponse(t *testing.T) {
	// given
	sock, _ := fakeBroker(t, func(conn net.Conn, _ protocol.Request) {
		conn.Write([]byte(`{"error":"` + strings.Repeat("x", MaxResponseBytes) + `"}` + "\n"))
	})

	// when
	_, err := Call(context.Background(), sock, protocol.Request{Op: protocol.OpListActions})

	// then
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallRejectsMalformedResponse(t *testing.T) {
	// given
	sock, _ := fakeBroker(t, func(conn net.Conn, _ protocol.Request) { conn.Write([]byte("not json\n")) })

	// when
	_, err := Call(context.Background(), sock, protocol.Request{Op: protocol.OpListActions})

	// then
	var brokerErr *BrokerError
	if err == nil || errors.As(err, &brokerErr) {
		t.Fatalf("err = %v", err)
	}
}
