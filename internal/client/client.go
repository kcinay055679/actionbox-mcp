// Package client sends one request to the broker socket (spec §8.5) and reads its response.
package client

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

	"actionbox/internal/protocol"
)

const MaxResponseBytes = 1 << 20

type BrokerError struct {
	Message string
}

func (e *BrokerError) Error() string { return e.Message }

func Call(ctx context.Context, socketPath string, req protocol.Request) (protocol.Response, error) {
	if req.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return protocol.Response{}, fmt.Errorf("get cwd: %w", err)
		}
		req.Cwd = wd
	}
	line, err := json.Marshal(req)
	if err != nil {
		return protocol.Response{}, err
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return protocol.Response{}, err
	}
	defer conn.Close()
	// closing unblocks read/write on cancel without deadline
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	resp, err := exchange(conn, append(line, '\n'))
	if err != nil {
		if ctx.Err() != nil {
			return protocol.Response{}, ctx.Err()
		}
		return protocol.Response{}, err
	}
	if resp.Error != "" {
		return resp, &BrokerError{Message: resp.Error}
	}
	return resp, nil
}

func exchange(conn net.Conn, line []byte) (protocol.Response, error) {
	if _, err := conn.Write(line); err != nil {
		return protocol.Response{}, fmt.Errorf("send request: %w", err)
	}
	reader := bufio.NewReader(io.LimitReader(conn, MaxResponseBytes+1))
	answer, err := reader.ReadBytes('\n')
	if len(answer) > MaxResponseBytes {
		return protocol.Response{}, fmt.Errorf("broker response too large (> %d bytes)", MaxResponseBytes)
	}
	if err != nil && !(errors.Is(err, io.EOF) && len(bytes.TrimSpace(answer)) > 0) {
		return protocol.Response{}, fmt.Errorf("read response: %w", err)
	}
	var resp protocol.Response
	if err := json.Unmarshal(answer, &resp); err != nil {
		return protocol.Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}
