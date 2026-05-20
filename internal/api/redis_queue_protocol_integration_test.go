package api

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

func startRedisMuxListener(t *testing.T, server *Server) (addr string, stop func()) {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.acceptMuxConnections(listener, nil)
	}()

	stop = func() {
		_ = listener.Close()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("accept loop returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("timeout waiting for accept loop to exit")
		}
	}

	return listener.Addr().String(), stop
}

func writeTestRESPCommand(conn net.Conn, args ...string) error {
	if conn == nil {
		return net.ErrClosed
	}
	if len(args) == 0 {
		return nil
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&buf, "$%d\r\n%s\r\n", len(arg), arg)
	}
	_, err := conn.Write(buf.Bytes())
	return err
}

func readTestRESPLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", fmt.Errorf("invalid RESP line terminator: %q", line)
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func readTestRESPError(r *bufio.Reader) (string, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	if prefix != '-' {
		return "", fmt.Errorf("expected error prefix '-', got %q", prefix)
	}
	return readTestRESPLine(r)
}

func readTestRESPSimpleString(r *bufio.Reader) (string, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	if prefix != '+' {
		return "", fmt.Errorf("expected simple string prefix '+', got %q", prefix)
	}
	return readTestRESPLine(r)
}

func readTestRESPBulkString(r *bufio.Reader) ([]byte, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '$' {
		return nil, fmt.Errorf("expected bulk string prefix '$', got %q", prefix)
	}

	line, err := readTestRESPLine(r)
	if err != nil {
		return nil, err
	}
	length, err := strconv.Atoi(line)
	if err != nil {
		return nil, fmt.Errorf("invalid bulk string length %q: %v", line, err)
	}
	if length == -1 {
		return nil, nil
	}
	if length < -1 {
		return nil, fmt.Errorf("invalid bulk string length %d", length)
	}

	payload := make([]byte, length+2)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if payload[length] != '\r' || payload[length+1] != '\n' {
		return nil, fmt.Errorf("invalid bulk string terminator")
	}
	return payload[:length], nil
}

func readRESPArrayOfBulkStrings(r *bufio.Reader) ([][]byte, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '*' {
		return nil, fmt.Errorf("expected array prefix '*', got %q", prefix)
	}

	line, err := readTestRESPLine(r)
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(line)
	if err != nil {
		return nil, fmt.Errorf("invalid array length %q: %v", line, err)
	}
	if count < 0 {
		return nil, fmt.Errorf("invalid array length %d", count)
	}

	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		item, err := readTestRESPBulkString(r)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func readTestRESPInteger(r *bufio.Reader) (int, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if prefix != ':' {
		return 0, fmt.Errorf("expected integer prefix ':', got %q", prefix)
	}

	line, err := readTestRESPLine(r)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q: %v", line, err)
	}
	return value, nil
}

func readTestRESPArrayHeader(r *bufio.Reader) (int, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if prefix != '*' {
		return 0, fmt.Errorf("expected array prefix '*', got %q", prefix)
	}

	line, err := readTestRESPLine(r)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("invalid array length %q: %v", line, err)
	}
	if count < 0 {
		return 0, fmt.Errorf("invalid array length %d", count)
	}
	return count, nil
}

func readTestRESPPubSubSubscribe(r *bufio.Reader) (string, int, error) {
	count, err := readTestRESPArrayHeader(r)
	if err != nil {
		return "", 0, err
	}
	if count != 3 {
		return "", 0, fmt.Errorf("subscribe array length = %d, want 3", count)
	}

	kind, err := readTestRESPBulkString(r)
	if err != nil {
		return "", 0, err
	}
	if string(kind) != "subscribe" {
		return "", 0, fmt.Errorf("pubsub kind = %q, want subscribe", string(kind))
	}

	channel, err := readTestRESPBulkString(r)
	if err != nil {
		return "", 0, err
	}
	subscriptions, err := readTestRESPInteger(r)
	if err != nil {
		return "", 0, err
	}
	return string(channel), subscriptions, nil
}

func readTestRESPPubSubMessage(r *bufio.Reader) (string, []byte, error) {
	count, err := readTestRESPArrayHeader(r)
	if err != nil {
		return "", nil, err
	}
	if count != 3 {
		return "", nil, fmt.Errorf("message array length = %d, want 3", count)
	}

	kind, err := readTestRESPBulkString(r)
	if err != nil {
		return "", nil, err
	}
	if string(kind) != "message" {
		return "", nil, fmt.Errorf("pubsub kind = %q, want message", string(kind))
	}

	channel, err := readTestRESPBulkString(r)
	if err != nil {
		return "", nil, err
	}
	payload, err := readTestRESPBulkString(r)
	if err != nil {
		return "", nil, err
	}
	return string(channel), payload, nil
}

func TestRedisProtocol_ManagementDisabled_RejectsConnection(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	redisqueue.SetEnabled(false)

	server := newTestServer(t)
	if server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be false")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if errWrite := writeTestRESPCommand(conn, "PING"); errWrite != nil {
		t.Fatalf("failed to write RESP command: %v", errWrite)
	}

	if msg, err := readTestRESPError(bufio.NewReader(conn)); err != nil {
		t.Fatalf("failed to read disabled RESP error: %v", err)
	} else if msg != "ERR RESP AUTH disabled; use mTLS" {
		t.Fatalf("unexpected disabled RESP error: %q", msg)
	}

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed after disabled RESP error")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed after disabled RESP error, got timeout: %v", errRead)
	}
}

func TestRedisProtocol_HomeEnabled_DisablesConnection(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-password")
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}
	if server.cfg == nil {
		t.Fatalf("expected server cfg to be non-nil")
	}
	server.cfg.Home.Enabled = true
	redisqueue.SetEnabled(true)

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = writeTestRESPCommand(conn, "PING")

	if msg, err := readTestRESPError(bufio.NewReader(conn)); err != nil {
		t.Fatalf("failed to read disabled RESP error: %v", err)
	} else if msg != "ERR RESP AUTH disabled; use mTLS" {
		t.Fatalf("unexpected disabled RESP error: %q", msg)
	}

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed after disabled RESP error")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed after disabled RESP error, got timeout: %v", errRead)
	}
}

func TestRedisProtocol_AUTH_DisabledAndClosesConnection(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader := bufio.NewReader(conn)

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, err := readTestRESPError(reader); err != nil {
		t.Fatalf("failed to read disabled AUTH error: %v", err)
	} else if msg != "ERR RESP AUTH disabled; use mTLS" {
		t.Fatalf("unexpected disabled AUTH error: %q", msg)
	}

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed after disabled AUTH error")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed after disabled AUTH error, got timeout: %v", errRead)
	}
}

func TestRedisProtocol_DisabledRESPCommandsRejectAndClose(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-password")
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	cases := []struct {
		name string
		args []string
	}{
		{name: "AUTH", args: []string{"AUTH", "test-management-password"}},
		{name: "LPOP", args: []string{"LPOP", "queue"}},
		{name: "SUBSCRIBE", args: []string{"SUBSCRIBE", "usage"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, errDial := net.DialTimeout("tcp", addr, time.Second)
			if errDial != nil {
				t.Fatalf("failed to dial redis listener: %v", errDial)
			}
			t.Cleanup(func() { _ = conn.Close() })

			reader := bufio.NewReader(conn)
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

			if errWrite := writeTestRESPCommand(conn, tc.args...); errWrite != nil {
				t.Fatalf("failed to write %s command: %v", tc.name, errWrite)
			}
			if msg, err := readTestRESPError(reader); err != nil {
				t.Fatalf("failed to read disabled RESP error for %s: %v", tc.name, err)
			} else if msg != "ERR RESP AUTH disabled; use mTLS" {
				t.Fatalf("unexpected disabled RESP error for %s: %q", tc.name, msg)
			}

			buf := make([]byte, 1)
			_, errRead := conn.Read(buf)
			if errRead == nil {
				t.Fatalf("expected connection to be closed after disabled RESP error for %s", tc.name)
			}
			if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
				t.Fatalf("expected connection to be closed after disabled RESP error for %s, got timeout: %v", tc.name, errRead)
			}
		})
	}
}
