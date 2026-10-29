package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"actionbox/internal/protocol"
)

func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func removeStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		conn.Close()
		return fmt.Errorf("a broker is already listening on %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe %s: %w", path, err)
	}
	return os.Remove(path)
}

func peerUID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}

func (b *Broker) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	uid, err := b.opts.PeerUID(conn)
	if err != nil || uid != os.Getuid() {
		b.display("refused connection: peer uid %d, %v", uid, err)
		return
	}
	conn.SetDeadline(time.Now().Add(b.opts.RequestTimeout))
	req, err := readRequest(conn)
	var resp protocol.Response
	if err != nil {
		b.display("bad request: %v", err)
		resp = protocol.Response{Error: "bad request: " + err.Error()}
	} else {
		conn.SetDeadline(time.Now().Add(b.opts.RequestTimeout + b.opts.Wait))
		resp = b.handle(ctx, req)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		raw, _ = json.Marshal(protocol.Response{Error: "internal error"})
	}
	conn.Write(append(raw, '\n'))
}

func readRequest(conn net.Conn) (protocol.Request, error) {
	r := bufio.NewReader(io.LimitReader(conn, protocol.MaxRequestBytes+1))
	line, err := r.ReadBytes('\n')
	if len(line) > protocol.MaxRequestBytes {
		return protocol.Request{}, fmt.Errorf("larger than %d bytes", protocol.MaxRequestBytes)
	}
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return protocol.Request{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var req protocol.Request
	if err := dec.Decode(&req); err != nil {
		return protocol.Request{}, err
	}
	return req, nil
}
