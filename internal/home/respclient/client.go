// Package respclient is the small RESP2 client used by the Home compatibility
// transport. It intentionally has no dependency on a Redis client library:
// Home speaks a Redis-compatible wire protocol, while CliRelay owns the
// PostgreSQL-backed runtime state.
package respclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	Nil            = errors.New("redis: nil")
	ErrClosed      = net.ErrClosed
	ErrPoolTimeout = errors.New("redis: connection pool timeout")
)

type Error interface {
	error
	RedisError()
}

type serverError string

func (e serverError) Error() string { return string(e) }
func (serverError) RedisError()     {}

type Options struct {
	Addr                  string
	Password              string
	DialTimeout           time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	MaxRetries            int
	DialerRetries         int
	PoolSize              int
	ContextTimeoutEnabled bool
	TLSConfig             *tls.Config
	Dialer                func(context.Context, string, string) (net.Conn, error)
	Protocol              int
	DisableIdentity       bool
}

func NewDialer(options *Options) func(context.Context, string, string) (net.Conn, error) {
	var copyOptions Options
	if options != nil {
		copyOptions = *options
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		dialer := &net.Dialer{Timeout: copyOptions.DialTimeout, KeepAlive: 5 * time.Minute}
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if copyOptions.TLSConfig == nil {
			return conn, nil
		}
		tlsConn := tls.Client(conn, copyOptions.TLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
}

type Client struct {
	options *Options
	mu      sync.Mutex
	conn    net.Conn
	reader  *bufio.Reader
	closed  bool
	timeout time.Duration
	hello   bool
}

func NewClient(options *Options) *Client {
	if options == nil {
		options = &Options{}
	}
	copyOptions := *options
	return &Client{options: &copyOptions}
}

func (c *Client) clone() *Client {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	options := *c.options
	timeout := c.timeout
	hello := c.hello
	c.mu.Unlock()
	return &Client{options: &options, timeout: timeout, hello: hello}
}

func (c *Client) WithTimeout(timeout time.Duration) *Client {
	clone := c.clone()
	if clone != nil {
		clone.timeout = timeout
	}
	return clone
}

func (c *Client) Options() *Options {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.options == nil {
		return nil
	}
	copyOptions := *c.options
	return &copyOptions
}

func (c *Client) Conn() *Client {
	clone := c.clone()
	if clone != nil {
		clone.hello = true
	}
	return clone
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.reader = nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	options := *c.options
	c.mu.Unlock()
	address := options.Addr
	dialer := options.Dialer
	if dialer == nil {
		dialer = NewDialer(&options)
	}
	return dialer(ctx, "tcp", address)
}

func (c *Client) ensureConn(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if c.conn != nil && c.reader != nil {
		conn, reader := c.conn, c.reader
		c.mu.Unlock()
		return conn, reader, nil
	}
	c.mu.Unlock()
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return nil, nil, ErrClosed
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	reader := c.reader
	c.mu.Unlock()
	return conn, reader, nil
}

func (c *Client) deadline(ctx context.Context, timeout time.Duration) (time.Time, bool) {
	var deadline time.Time
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if timeout > 0 {
		candidate := time.Now().Add(timeout)
		if deadline.IsZero() || candidate.Before(deadline) {
			deadline = candidate
		}
	}
	return deadline, !deadline.IsZero()
}

func (c *Client) execute(ctx context.Context, args ...any) *Cmd {
	cmd := &Cmd{args: args}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		cmd.err = err
		return cmd
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		cmd.err = ErrClosed
		return cmd
	}
	conn, reader, err := c.ensureConnLocked(ctx)
	if err != nil {
		cmd.err = err
		return cmd
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = c.options.ReadTimeout
		if c.options.WriteTimeout > timeout {
			timeout = c.options.WriteTimeout
		}
	}
	deadline, hasDeadline := c.deadline(ctx, timeout)
	if hasDeadline {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	if c.hello {
		if err := writeCommand(conn, []any{"HELLO", 3}); err != nil {
			cmd.err = err
			return cmd
		}
		if hello, err := readRESP(reader); err != nil {
			cmd.err = err
			return cmd
		} else if hello.err != nil {
			cmd.err = hello.err
			return cmd
		}
		c.hello = false
	}
	if err := writeCommand(conn, args); err != nil {
		cmd.err = err
		return cmd
	}
	value, err := readRESP(reader)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			c.conn = nil
			c.reader = nil
		}
		cmd.err = err
		return cmd
	}
	cmd.value, cmd.err = value.value, value.err
	return cmd
}

func (c *Client) ensureConnLocked(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	if c.conn != nil && c.reader != nil {
		return c.conn, c.reader, nil
	}
	options := *c.options
	dialer := options.Dialer
	if dialer == nil {
		dialer = NewDialer(&options)
	}
	conn, err := dialer(ctx, "tcp", options.Addr)
	if err != nil {
		return nil, nil, err
	}
	if c.closed {
		_ = conn.Close()
		return nil, nil, ErrClosed
	}
	reader := bufio.NewReader(conn)
	c.conn = conn
	c.reader = reader
	return c.conn, c.reader, nil
}

func (c *Client) Ping(ctx context.Context) *Cmd { return c.execute(ctx, "ping") }
func (c *Client) Get(ctx context.Context, key string) *StringCmd {
	return stringCmd(c.execute(ctx, "get", key))
}
func (c *Client) Set(ctx context.Context, key string, value any, args ...any) *Cmd {
	command := []any{"set", key, value}
	command = append(command, args...)
	return c.execute(ctx, command...)
}
func (c *Client) Del(ctx context.Context, keys ...string) *IntCmd {
	command := []any{"del"}
	for _, key := range keys {
		command = append(command, key)
	}
	return intCmd(c.execute(ctx, command...))
}
func (c *Client) Expire(ctx context.Context, key string, ttl time.Duration) *BoolCmd {
	seconds := int64(ttl / time.Second)
	if ttl > 0 && seconds == 0 {
		seconds = 1
	}
	return boolCmd(c.execute(ctx, "expire", key, seconds))
}
func (c *Client) TTL(ctx context.Context, key string) *DurationCmd {
	return durationCmd(c.execute(ctx, "ttl", key))
}
func (c *Client) IncrBy(ctx context.Context, key string, delta int64) *IntCmd {
	return intCmd(c.execute(ctx, "incrby", key, delta))
}
func (c *Client) MGet(ctx context.Context, keys ...string) *SliceCmd {
	command := []any{"mget"}
	for _, key := range keys {
		command = append(command, key)
	}
	return sliceCmd(c.execute(ctx, command...))
}
func (c *Client) MSet(ctx context.Context, values ...any) *Cmd {
	command := append([]any{"mset"}, values...)
	return c.execute(ctx, command...)
}
func (c *Client) LPush(ctx context.Context, key string, values ...any) *IntCmd {
	command := []any{"LPUSH", key}
	command = append(command, values...)
	return intCmd(c.execute(ctx, command...))
}
func (c *Client) RPush(ctx context.Context, key string, values ...any) *IntCmd {
	command := []any{"rpush", key}
	command = append(command, values...)
	return intCmd(c.execute(ctx, command...))
}
func (c *Client) RPop(ctx context.Context, key string) *StringCmd {
	return stringCmd(c.execute(ctx, "rpop", key))
}
func (c *Client) Do(ctx context.Context, args ...any) *Cmd { return c.execute(ctx, args...) }

func (c *Client) Subscribe(ctx context.Context, channels ...string) *PubSub {
	pub := &PubSub{client: c.clone(), channels: append([]string(nil), channels...)}
	if err := pub.open(ctx); err != nil {
		pub.err = err
	}
	return pub
}

func (c *Client) Process(ctx context.Context, command Cmder) error {
	if command == nil {
		return ErrClosed
	}
	args := command.commandArgs()
	if len(args) == 0 {
		return ErrClosed
	}
	result := c.execute(ctx, args...)
	command.setResult(result.value, result.err)
	return result.err
}

type Cmder interface {
	Err() error
	commandArgs() []any
	setResult(any, error)
}

type Cmd struct {
	args  []any
	value any
	err   error
}

func (c *Cmd) commandArgs() []any { return c.args }
func (c *Cmd) setResult(value any, err error) {
	c.value, c.err = value, err
}
func (c *Cmd) Err() error {
	if c == nil {
		return ErrClosed
	}
	return c.err
}
func (c *Cmd) Result() (any, error) {
	if c == nil {
		return nil, ErrClosed
	}
	return c.value, c.err
}
func (c *Cmd) Text() (string, error) {
	if c == nil {
		return "", ErrClosed
	}
	if c.err != nil {
		return "", c.err
	}
	switch value := c.value.(type) {
	case nil:
		return "", Nil
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return fmt.Sprint(value), nil
	}
}
func (c *Cmd) Bytes() ([]byte, error) {
	text, err := c.Text()
	return []byte(text), err
}
func (c *Cmd) Int64() (int64, error) {
	if c == nil {
		return 0, ErrClosed
	}
	if c.err != nil {
		return 0, c.err
	}
	switch value := c.value.(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case string:
		return strconv.ParseInt(value, 10, 64)
	case []byte:
		return strconv.ParseInt(string(value), 10, 64)
	default:
		return 0, fmt.Errorf("respclient: expected integer, got %T", c.value)
	}
}

type StringCmd struct{ Cmd }

func NewStringCmd(_ context.Context, args ...any) *StringCmd { return &StringCmd{Cmd: Cmd{args: args}} }
func stringCmd(cmd *Cmd) *StringCmd {
	if cmd == nil {
		return &StringCmd{Cmd: Cmd{err: ErrClosed}}
	}
	return &StringCmd{Cmd: *cmd}
}
func (c *StringCmd) SetVal(value string) { c.value, c.err = value, nil }

type IntCmd struct{ Cmd }

func intCmd(cmd *Cmd) *IntCmd            { return &IntCmd{Cmd: *cmd} }
func (c *IntCmd) Result() (int64, error) { return c.Int64() }

type BoolCmd struct{ Cmd }

func boolCmd(cmd *Cmd) *BoolCmd { return &BoolCmd{Cmd: *cmd} }
func (c *BoolCmd) Result() (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	if c.value == nil {
		return false, Nil
	}
	n, err := c.Int64()
	if err != nil {
		return false, err
	}
	return n != 0, nil
}

type DurationCmd struct{ Cmd }

func durationCmd(cmd *Cmd) *DurationCmd { return &DurationCmd{Cmd: *cmd} }
func (c *DurationCmd) Result() (time.Duration, error) {
	n, err := c.Int64()
	if err != nil {
		return 0, err
	}
	return time.Duration(n) * time.Second, nil
}

type SliceCmd struct{ Cmd }

func sliceCmd(cmd *Cmd) *SliceCmd { return &SliceCmd{Cmd: *cmd} }
func (c *SliceCmd) Result() ([]any, error) {
	if c.err != nil {
		return nil, c.err
	}
	values, ok := c.value.([]any)
	if !ok {
		return nil, fmt.Errorf("respclient: expected array, got %T", c.value)
	}
	return values, nil
}

type PubSub struct {
	client   *Client
	channels []string
	conn     net.Conn
	reader   *bufio.Reader
	mu       sync.Mutex
	writeMu  sync.Mutex
	closed   bool
	err      error
}

func (p *PubSub) open(ctx context.Context) error {
	if p == nil || p.client == nil {
		return ErrClosed
	}
	conn, err := p.client.dial(ctx)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	if err := setupConnection(conn, reader, p.client.options); err != nil {
		_ = conn.Close()
		return err
	}
	if err := writeCommand(conn, append([]any{"subscribe"}, stringsToAny(p.channels)...)); err != nil {
		_ = conn.Close()
		return err
	}
	p.conn, p.reader = conn, reader
	return nil
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func (p *PubSub) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	conn := p.conn
	p.conn = nil
	p.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (p *PubSub) Ping(ctx context.Context, payload ...string) error {
	if p == nil {
		return ErrClosed
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	if p.closed || p.conn == nil {
		p.mu.Unlock()
		return ErrClosed
	}
	conn := p.conn
	p.mu.Unlock()
	command := []any{"PING"}
	if len(payload) > 0 {
		command = append(command, payload[0])
	}
	if err := setConnDeadline(conn, ctx, p.client.options.ReadTimeout); err != nil {
		return err
	}
	if err := writeCommand(conn, command); err != nil {
		originalErr := err
		// Preserve compatible PubSub behavior: replace a broken transport before
		// returning the write failure, so the next Receive observes the new
		// subscription ACK instead of using a dead connection.
		_ = p.reconnectIfCurrent(ctx, conn)
		return originalErr
	}
	return nil
}

func (p *PubSub) reconnectIfCurrent(ctx context.Context, failed net.Conn) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.client == nil {
		return ErrClosed
	}
	if p.conn != failed {
		return nil
	}
	return p.reconnectLocked(ctx)
}

func (p *PubSub) reconnectLocked(ctx context.Context) error {
	if p == nil || p.client == nil || p.closed {
		return ErrClosed
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	conn, err := p.client.dial(ctx)
	if err != nil {
		p.conn = nil
		p.reader = nil
		p.err = err
		return err
	}
	reader := bufio.NewReader(conn)
	if err := setupConnection(conn, reader, p.client.options); err != nil {
		_ = conn.Close()
		p.conn = nil
		p.reader = nil
		p.err = err
		return err
	}
	if err := writeCommand(conn, append([]any{"subscribe"}, stringsToAny(p.channels)...)); err != nil {
		_ = conn.Close()
		p.conn = nil
		p.reader = nil
		p.err = err
		return err
	}
	p.conn = conn
	p.reader = reader
	p.err = nil
	return nil
}

func (p *PubSub) Receive(ctx context.Context) (any, error) {
	return p.ReceiveTimeout(ctx, 0)
}

func (p *PubSub) ReceiveTimeout(ctx context.Context, timeout time.Duration) (any, error) {
	if p == nil {
		return nil, ErrClosed
	}
	p.mu.Lock()
	if p.closed || p.conn == nil {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	conn, reader := p.conn, p.reader
	p.mu.Unlock()
	if err := setConnDeadline(conn, ctx, timeout); err != nil {
		return nil, err
	}
	value, err := readRESP(reader)
	if err != nil {
		originalErr := err
		if reconnectable(err) {
			p.writeMu.Lock()
			_ = p.reconnectIfCurrent(ctx, conn)
			p.writeMu.Unlock()
		}
		return nil, originalErr
	}
	if value.err != nil {
		originalErr := value.err
		if reconnectable(originalErr) {
			p.writeMu.Lock()
			_ = p.reconnectIfCurrent(ctx, conn)
			p.writeMu.Unlock()
		}
		return nil, originalErr
	}
	return decodePubSubEvent(value.value)
}

func reconnectable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var timeout net.Error
	if errors.As(err, &timeout) {
		return !timeout.Timeout()
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

type Message struct {
	Channel string
	Payload string
}

type Pong struct{ Payload string }
type Subscription struct {
	Kind    string
	Channel string
	Count   int
}

func decodePubSubEvent(value any) (any, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return value, nil
	}
	kind := strings.ToLower(stringValue(items[0]))
	switch kind {
	case "message", "pmessage":
		if len(items) >= 3 {
			offset := 1
			if kind == "pmessage" {
				offset = 2
			}
			return &Message{Channel: stringValue(items[offset]), Payload: stringValue(items[offset+1])}, nil
		}
	case "subscribe", "psubscribe", "unsubscribe", "punsubscribe":
		if len(items) >= 3 {
			count, _ := intValue(items[2])
			return &Subscription{Kind: kind, Channel: stringValue(items[1]), Count: int(count)}, nil
		}
	case "pong":
		if len(items) >= 2 {
			return &Pong{Payload: stringValue(items[1])}, nil
		}
	}
	return value, nil
}

func setConnDeadline(conn net.Conn, ctx context.Context, timeout time.Duration) error {
	if conn == nil {
		return ErrClosed
	}
	var deadline time.Time
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if timeout > 0 {
		d := time.Now().Add(timeout)
		if deadline.IsZero() || d.Before(deadline) {
			deadline = d
		}
	}
	return conn.SetDeadline(deadline)
}

func setupConnection(conn net.Conn, reader *bufio.Reader, options *Options) error {
	if options == nil || options.Protocol != 3 {
		return nil
	}
	timeout := options.ReadTimeout
	if options.WriteTimeout > timeout {
		timeout = options.WriteTimeout
	}
	if err := setConnDeadline(conn, context.Background(), timeout); err != nil {
		return err
	}
	if err := writeCommand(conn, []any{"hello", 3}); err != nil {
		return err
	}
	value, err := readRESP(reader)
	if err != nil {
		return err
	}
	return value.err
}

type respValue struct {
	value any
	err   error
}

func writeCommand(w io.Writer, args []any) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "*%d\r\n", len(args))
	for _, arg := range args {
		raw := argumentBytes(arg)
		fmt.Fprintf(&buf, "$%d\r\n", len(raw))
		buf.Write(raw)
		buf.WriteString("\r\n")
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func argumentBytes(value any) []byte {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return typed
	case string:
		return []byte(typed)
	case int:
		return []byte(strconv.Itoa(typed))
	case int64:
		return []byte(strconv.FormatInt(typed, 10))
	case uint64:
		return []byte(strconv.FormatUint(typed, 10))
	case time.Duration:
		return []byte(strconv.FormatInt(int64(typed), 10))
	default:
		return []byte(fmt.Sprint(value))
	}
}

func readRESP(reader *bufio.Reader) (respValue, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return respValue{}, err
	}
	switch prefix {
	case '+':
		line, err := readLine(reader)
		return respValue{value: line, err: err}, err
	case '-':
		line, err := readLine(reader)
		if err != nil {
			return respValue{}, err
		}
		return respValue{err: serverError(line)}, nil
	case ':':
		line, err := readLine(reader)
		if err != nil {
			return respValue{}, err
		}
		value, err := strconv.ParseInt(line, 10, 64)
		return respValue{value: value, err: err}, err
	case '$':
		return readBulk(reader)
	case '*':
		return readArray(reader)
	case '%':
		return readMap(reader)
	case '_':
		if _, err := readLine(reader); err != nil {
			return respValue{}, err
		}
		return respValue{value: nil}, nil
	default:
		return respValue{}, fmt.Errorf("respclient: unsupported RESP prefix %q", prefix)
	}
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

func readBulk(reader *bufio.Reader) (respValue, error) {
	line, err := readLine(reader)
	if err != nil {
		return respValue{}, err
	}
	length, err := strconv.Atoi(line)
	if err != nil {
		return respValue{}, err
	}
	if length < 0 {
		return respValue{value: nil}, nil
	}
	data := make([]byte, length+2)
	if _, err := io.ReadFull(reader, data); err != nil {
		return respValue{}, err
	}
	if !bytes.Equal(data[length:], []byte("\r\n")) {
		return respValue{}, fmt.Errorf("respclient: invalid bulk terminator")
	}
	return respValue{value: append([]byte(nil), data[:length]...)}, nil
}

func readArray(reader *bufio.Reader) (respValue, error) {
	line, err := readLine(reader)
	if err != nil {
		return respValue{}, err
	}
	count, err := strconv.Atoi(line)
	if err != nil {
		return respValue{}, err
	}
	if count < 0 {
		return respValue{value: nil}, nil
	}
	values := make([]any, count)
	for i := range values {
		item, err := readRESP(reader)
		if err != nil {
			return respValue{}, err
		}
		if item.err != nil {
			return respValue{}, item.err
		}
		values[i] = item.value
	}
	return respValue{value: values}, nil
}

func readMap(reader *bufio.Reader) (respValue, error) {
	line, err := readLine(reader)
	if err != nil {
		return respValue{}, err
	}
	count, err := strconv.Atoi(line)
	if err != nil {
		return respValue{}, err
	}
	for i := 0; i < count*2; i++ {
		if _, err := readRESP(reader); err != nil {
			return respValue{}, err
		}
	}
	return respValue{value: "OK"}, nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

func intValue(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("respclient: expected integer, got %T", value)
	}
}
