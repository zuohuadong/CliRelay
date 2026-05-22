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
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return "", errRead
	}
	if prefix != '+' {
		return "", fmt.Errorf("expected simple string prefix '+', got %q", prefix)
	}
	return readTestRESPLine(r)
}

func readTestRESPBulkString(r *bufio.Reader) ([]byte, error) {
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return nil, errRead
	}
	if prefix != '$' {
		return nil, fmt.Errorf("expected bulk string prefix '$', got %q", prefix)
	}

	line, errLine := readTestRESPLine(r)
	if errLine != nil {
		return nil, errLine
	}
	length, errParse := strconv.Atoi(line)
	if errParse != nil {
		return nil, fmt.Errorf("invalid bulk string length %q: %v", line, errParse)
	}
	if length == -1 {
		return nil, nil
	}
	if length < -1 {
		return nil, fmt.Errorf("invalid bulk string length %d", length)
	}

	payload := make([]byte, length+2)
	if _, errRead := io.ReadFull(r, payload); errRead != nil {
		return nil, errRead
	}
	if payload[length] != '\r' || payload[length+1] != '\n' {
		return nil, fmt.Errorf("invalid bulk string terminator")
	}
	return payload[:length], nil
}

func readRESPArrayOfBulkStrings(r *bufio.Reader) ([][]byte, error) {
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return nil, errRead
	}
	if prefix != '*' {
		return nil, fmt.Errorf("expected array prefix '*', got %q", prefix)
	}

	line, errLine := readTestRESPLine(r)
	if errLine != nil {
		return nil, errLine
	}
	count, errParse := strconv.Atoi(line)
	if errParse != nil {
		return nil, fmt.Errorf("invalid array length %q: %v", line, errParse)
	}
	if count < 0 {
		return nil, fmt.Errorf("invalid array length %d", count)
	}

	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		item, errItem := readTestRESPBulkString(r)
		if errItem != nil {
			return nil, errItem
		}
		out = append(out, item)
	}
	return out, nil
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

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed when management is disabled")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed when management is disabled, got timeout: %v", errRead)
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

<<<<<<< HEAD
	reader := bufio.NewReader(conn)
	msg, errRead := readTestRESPError(reader)
	if errRead != nil {
		t.Fatalf("read home mode redis error: %v", errRead)
	}
	if msg != "ERR redis usage output disabled in home mode" {
		t.Fatalf("home mode redis error = %q", msg)
=======
	if msg, err := readTestRESPError(bufio.NewReader(conn)); err != nil {
		t.Fatalf("failed to read home-mode RESP error: %v", err)
	} else if msg != "ERR redis usage output disabled in home mode" {
		t.Fatalf("unexpected disabled RESP error: %q", msg)
	}

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed after home-mode RESP error")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed after home-mode RESP error, got timeout: %v", errRead)
>>>>>>> upstream/main
	}
}

func TestRedisProtocol_AUTH_And_PopContracts(t *testing.T) {
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
	if msg, errRead := readTestRESPSimpleString(reader); errRead != nil {
		t.Fatalf("failed to read AUTH response: %v", errRead)
	} else if msg != "OK" {
		t.Fatalf("unexpected AUTH response: %q", msg)
	}

	if !redisqueue.Enabled() {
		t.Fatalf("expected redisqueue to be enabled")
	}
	redisqueue.Enqueue([]byte("a"))
	redisqueue.Enqueue([]byte("b"))
	redisqueue.Enqueue([]byte("c"))

	if errWrite := writeTestRESPCommand(conn, "RPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write RPOP command: %v", errWrite)
	}
	if item, errRead := readTestRESPBulkString(reader); errRead != nil {
		t.Fatalf("failed to read RPOP response: %v", errRead)
	} else if string(item) != "a" {
		t.Fatalf("unexpected RPOP item: %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write LPOP command: %v", errWrite)
	}
	if item, errRead := readTestRESPBulkString(reader); errRead != nil {
		t.Fatalf("failed to read LPOP response: %v", errRead)
	} else if string(item) != "b" {
		t.Fatalf("unexpected LPOP item: %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "usage", "10"); errWrite != nil {
		t.Fatalf("failed to write RPOP count command: %v", errWrite)
	}
	items, errItems := readRESPArrayOfBulkStrings(reader)
	if errItems != nil {
		t.Fatalf("failed to read RPOP count response: %v", errItems)
	}
	if len(items) != 1 || string(items[0]) != "c" {
		t.Fatalf("unexpected RPOP count items: %#v", items)
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write LPOP empty command: %v", errWrite)
	}
	item, errItem := readTestRESPBulkString(reader)
	if errItem != nil {
		t.Fatalf("failed to read LPOP empty response: %v", errItem)
	}
	if item != nil {
		t.Fatalf("expected nil bulk string for empty queue, got %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "usage", "2"); errWrite != nil {
		t.Fatalf("failed to write RPOP empty count command: %v", errWrite)
	}
	emptyItems, errEmpty := readRESPArrayOfBulkStrings(reader)
	if errEmpty != nil {
		t.Fatalf("failed to read RPOP empty count response: %v", errEmpty)
	}
	if len(emptyItems) != 0 {
		t.Fatalf("expected empty array for empty queue with count, got %#v", emptyItems)
	}
}
<<<<<<< HEAD

func TestRedisProtocol_SubscribeUsageBroadcastsAndSkipsQueue(t *testing.T) {
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

	firstConn, errDialFirst := net.DialTimeout("tcp", addr, time.Second)
	if errDialFirst != nil {
		t.Fatalf("failed to dial first redis listener: %v", errDialFirst)
	}
	t.Cleanup(func() { _ = firstConn.Close() })
	firstReader := bufio.NewReader(firstConn)
	_ = firstConn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(firstConn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write first AUTH command: %v", errWrite)
	}
	if msg, err := readTestRESPSimpleString(firstReader); err != nil {
		t.Fatalf("failed to read first AUTH response: %v", err)
	} else if msg != "OK" {
		t.Fatalf("unexpected first AUTH response: %q", msg)
	}
	if errWrite := writeTestRESPCommand(firstConn, "SUBSCRIBE", "usage"); errWrite != nil {
		t.Fatalf("failed to write first SUBSCRIBE command: %v", errWrite)
	}
	if channel, count, err := readTestRESPPubSubSubscribe(firstReader); err != nil {
		t.Fatalf("failed to read first SUBSCRIBE response: %v", err)
	} else if channel != "usage" || count != 1 {
		t.Fatalf("unexpected first SUBSCRIBE response channel=%q count=%d", channel, count)
	}

	secondConn, errDialSecond := net.DialTimeout("tcp", addr, time.Second)
	if errDialSecond != nil {
		t.Fatalf("failed to dial second redis listener: %v", errDialSecond)
	}
	t.Cleanup(func() { _ = secondConn.Close() })
	secondReader := bufio.NewReader(secondConn)
	_ = secondConn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(secondConn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write second AUTH command: %v", errWrite)
	}
	if msg, err := readTestRESPSimpleString(secondReader); err != nil {
		t.Fatalf("failed to read second AUTH response: %v", err)
	} else if msg != "OK" {
		t.Fatalf("unexpected second AUTH response: %q", msg)
	}
	if errWrite := writeTestRESPCommand(secondConn, "SUBSCRIBE", "usage"); errWrite != nil {
		t.Fatalf("failed to write second SUBSCRIBE command: %v", errWrite)
	}
	if channel, count, err := readTestRESPPubSubSubscribe(secondReader); err != nil {
		t.Fatalf("failed to read second SUBSCRIBE response: %v", err)
	} else if channel != "usage" || count != 1 {
		t.Fatalf("unexpected second SUBSCRIBE response channel=%q count=%d", channel, count)
	}

	redisqueue.Enqueue([]byte(`{"id":1}`))

	if channel, payload, err := readTestRESPPubSubMessage(firstReader); err != nil {
		t.Fatalf("failed to read first pubsub message: %v", err)
	} else if channel != "usage" || string(payload) != `{"id":1}` {
		t.Fatalf("unexpected first pubsub message channel=%q payload=%q", channel, string(payload))
	}
	if channel, payload, err := readTestRESPPubSubMessage(secondReader); err != nil {
		t.Fatalf("failed to read second pubsub message: %v", err)
	} else if channel != "usage" || string(payload) != `{"id":1}` {
		t.Fatalf("unexpected second pubsub message channel=%q payload=%q", channel, string(payload))
	}

	popConn, errDialPop := net.DialTimeout("tcp", addr, time.Second)
	if errDialPop != nil {
		t.Fatalf("failed to dial pop redis listener: %v", errDialPop)
	}
	t.Cleanup(func() { _ = popConn.Close() })
	popReader := bufio.NewReader(popConn)
	_ = popConn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(popConn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write pop AUTH command: %v", errWrite)
	}
	if msg, err := readTestRESPSimpleString(popReader); err != nil {
		t.Fatalf("failed to read pop AUTH response: %v", err)
	} else if msg != "OK" {
		t.Fatalf("unexpected pop AUTH response: %q", msg)
	}
	if errWrite := writeTestRESPCommand(popConn, "LPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write pop LPOP command: %v", errWrite)
	}
	item, errItem := readTestRESPBulkString(popReader)
	if errItem != nil {
		t.Fatalf("failed to read pop LPOP response: %v", errItem)
	}
	if item != nil {
		t.Fatalf("expected subscribed usage to skip queue, got %q", string(item))
	}

	managementReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=1", nil)
	managementReq.Header.Set("Authorization", "Bearer "+managementPassword)
	managementRR := httptest.NewRecorder()
	server.engine.ServeHTTP(managementRR, managementReq)
	if managementRR.Code != http.StatusOK {
		t.Fatalf("management usage status = %d, want %d body=%s", managementRR.Code, http.StatusOK, managementRR.Body.String())
	}
	var managementPayload []json.RawMessage
	if errUnmarshal := json.Unmarshal(managementRR.Body.Bytes(), &managementPayload); errUnmarshal != nil {
		t.Fatalf("unmarshal management usage response: %v", errUnmarshal)
	}
	if len(managementPayload) != 0 {
		t.Fatalf("expected management usage queue to be empty, got %s", managementRR.Body.String())
	}
}

func TestRedisProtocol_IPBan_SkipsMissingCredentialsLikeManagementPolicy(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = serverConn.Close() })

	fakeRemote := &net.TCPAddr{
		IP:   net.ParseIP("1.2.3.4"),
		Port: 1234,
	}
	wrappedConn := &remoteAddrConn{Conn: serverConn, remoteAddr: fakeRemote}

	go server.handleRedisConnection(wrappedConn, bufio.NewReader(wrappedConn))

	reader := bufio.NewReader(clientConn)
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	for i := 0; i < 5; i++ {
		if errWrite := writeTestRESPCommand(clientConn, "LPOP", "queue"); errWrite != nil {
			t.Fatalf("failed to write LPOP command: %v", errWrite)
		}
		if msg, err := readTestRESPError(reader); err != nil {
			t.Fatalf("failed to read LPOP NOAUTH error: %v", err)
		} else if msg != "NOAUTH Authentication required." {
			t.Fatalf("unexpected LPOP NOAUTH error at attempt %d: %q", i+1, msg)
		}
	}

	if errWrite := writeTestRESPCommand(clientConn, "LPOP", "queue"); errWrite != nil {
		t.Fatalf("failed to write LPOP command after failures: %v", errWrite)
	}
	msg, err := readTestRESPError(reader)
	if err != nil {
		t.Fatalf("failed to read LPOP NOAUTH error: %v", err)
	}
	if msg != "NOAUTH Authentication required." {
		t.Fatalf("unexpected LPOP error after missing credentials attempts: %q", msg)
	}
}

func TestRedisProtocol_AUTH_IPBan_BlocksCorrectPasswordDuringBan(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = serverConn.Close() })

	fakeRemote := &net.TCPAddr{
		IP:   net.ParseIP("1.2.3.4"),
		Port: 1234,
	}
	wrappedConn := &remoteAddrConn{Conn: serverConn, remoteAddr: fakeRemote}

	go server.handleRedisConnection(wrappedConn, bufio.NewReader(wrappedConn))

	reader := bufio.NewReader(clientConn)
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	for i := 0; i < 5; i++ {
		if errWrite := writeTestRESPCommand(clientConn, "AUTH", "wrong-password"); errWrite != nil {
			t.Fatalf("failed to write AUTH command: %v", errWrite)
		}
		if msg, err := readTestRESPError(reader); err != nil {
			t.Fatalf("failed to read AUTH error: %v", err)
		} else if msg != "ERR invalid management key" {
			t.Fatalf("unexpected AUTH error at attempt %d: %q", i+1, msg)
		}
	}

	for i := 0; i < 2; i++ {
		if errWrite := writeTestRESPCommand(clientConn, "AUTH", "wrong-password"); errWrite != nil {
			t.Fatalf("failed to write AUTH command after failures: %v", errWrite)
		}
		msg, err := readTestRESPError(reader)
		if err != nil {
			t.Fatalf("failed to read AUTH banned error: %v", err)
		}
		if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
			t.Fatalf("unexpected AUTH banned error at attempt %d: %q", i+6, msg)
		}
	}

	if errWrite := writeTestRESPCommand(clientConn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command with correct password: %v", errWrite)
	}
	msg, err := readTestRESPError(reader)
	if err != nil {
		t.Fatalf("failed to read AUTH banned error for correct password: %v", err)
	}
	if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected AUTH banned error for correct password: %q", msg)
	}
}

func TestRedisProtocol_LOCALHOST_AUTH_IPBan_BlocksCorrectPasswordDuringBan(t *testing.T) {
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

	for i := 0; i < 5; i++ {
		if errWrite := writeTestRESPCommand(conn, "AUTH", "wrong-password"); errWrite != nil {
			t.Fatalf("failed to write AUTH command: %v", errWrite)
		}
		if msg, err := readTestRESPError(reader); err != nil {
			t.Fatalf("failed to read AUTH error: %v", err)
		} else if msg != "ERR invalid management key" {
			t.Fatalf("unexpected AUTH error at attempt %d: %q", i+1, msg)
		}
	}

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command with correct password: %v", errWrite)
	}
	msg, err := readTestRESPError(reader)
	if err != nil {
		t.Fatalf("failed to read AUTH banned error for correct password: %v", err)
	}
	if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected AUTH banned error for correct password: %q", msg)
	}
}
=======
>>>>>>> upstream/main
