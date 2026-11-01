package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

var ErrClosed = errors.New("app-server client is closed")

// Options configures a temporary app-server process.
type Options struct {
	Binary     string
	Dir        string
	Env        []string
	Timeout    time.Duration
	ClientName string
	Version    string
}

// Client owns one `codex app-server --listen stdio://` process.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer

	callMu  sync.Mutex
	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	done      chan struct{}
	readErrMu sync.Mutex
	readErr   error
	closing   atomic.Bool
	closeOnce sync.Once
	timeout   time.Duration

	Init *InitializeResult
}

// Start launches app-server, performs initialize with experimentalApi=true and
// returns a ready client. The caller must Close it.
func Start(ctx context.Context, opts Options) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	bin := opts.Binary
	if bin == "" {
		bin = os.Getenv("CXR_CODEX_BIN")
	}
	if bin == "" {
		bin = "codex"
	}
	cmd := exec.Command(bin, "app-server", "--listen", "stdio://")
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	cmd.SysProcAttr = sysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	tail := newTailBuffer(8192)
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start %s app-server: %w", bin, err)
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  tail,
		pending: make(map[int64]chan rpcMessage),
		done:    make(chan struct{}),
		timeout: opts.Timeout,
	}
	go c.readLoop()

	name := opts.ClientName
	if name == "" {
		name = "cxr"
	}
	version := opts.Version
	if version == "" {
		version = "0.1.0"
	}
	initCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	var res InitializeResult
	err = c.call(initCtx, "initialize", InitializeParams{
		ClientInfo:   ClientInfo{Name: name, Title: "codex-rewind", Version: version},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: true},
	}, &res)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize app-server: %w", err)
	}
	c.Init = &res
	return c, nil
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.ID == nil {
			continue // notifications are not needed for rewind
		}
		c.pendingMu.Lock()
		ch := c.pending[*msg.ID]
		c.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("app-server closed stdout")
	}
	c.setReadErr(err)
	close(c.done)
	c.failPending(fmt.Errorf("app-server exited: %w", err))
}

func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	chans := make([]chan rpcMessage, 0, len(c.pending))
	for _, ch := range c.pending {
		chans = append(chans, ch)
	}
	c.pendingMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- rpcMessage{Error: &RPCError{Code: -32000, Message: err.Error()}}:
		default:
		}
	}
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.callMu.Lock()
	defer c.callMu.Unlock()
	select {
	case <-c.done:
		return c.closedError()
	default:
	}
	if c.closing.Load() {
		return ErrClosed
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	data, err := json.Marshal(rpcRequest{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	_, werr := c.stdin.Write(data)
	c.writeMu.Unlock()
	if werr != nil {
		c.kill()
		return fmt.Errorf("write %s: %w", method, werr)
	}

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if out != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, out); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.kill()
		return ctx.Err()
	case <-timer.C:
		c.kill()
		return fmt.Errorf("app-server %s timed out after %s%s", method, c.timeout, c.stderrSuffix())
	case <-c.done:
		return c.closedError()
	}
}

func (c *Client) setReadErr(err error) {
	c.readErrMu.Lock()
	c.readErr = err
	c.readErrMu.Unlock()
}

func (c *Client) getReadErr() error {
	c.readErrMu.Lock()
	defer c.readErrMu.Unlock()
	return c.readErr
}

func (c *Client) closedError() error {
	err := ErrClosed
	if e := c.getReadErr(); e != nil {
		err = fmt.Errorf("%w: %v", ErrClosed, e)
	}
	if tail := c.stderr.String(); tail != "" {
		return fmt.Errorf("%w (stderr: %s)", err, tail)
	}
	return err
}

func (c *Client) stderrSuffix() string {
	if s := c.stderr.String(); s != "" {
		return " (stderr: " + s + ")"
	}
	return ""
}

// Initialize returns the handshake result.
func (c *Client) InitializeResult() *InitializeResult { return c.Init }

func (c *Client) ThreadList(ctx context.Context, p ThreadListParams) (*ThreadListResult, error) {
	var out ThreadListResult
	if p.Limit == 0 {
		p.Limit = 20
	}
	if len(p.SourceKinds) == 0 {
		p.SourceKinds = []string{SourceCLI, SourceVSCode, SourceExec, SourceAppServer}
	}
	if err := c.call(ctx, "thread/list", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ThreadTurnsList(ctx context.Context, p ThreadTurnsListParams) (*ThreadTurnsListResult, error) {
	var out ThreadTurnsListResult
	if p.Limit == 0 {
		p.Limit = 100
	}
	if p.SortDirection == "" {
		p.SortDirection = "asc"
	}
	if p.ItemsView == "" {
		p.ItemsView = "summary"
	}
	if err := c.call(ctx, "thread/turns/list", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ThreadFork(ctx context.Context, p ThreadForkParams) (*ThreadForkResult, error) {
	var out ThreadForkResult
	p.ExcludeTurns = true
	if err := c.call(ctx, "thread/fork", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Close shuts stdin, waits briefly and kills the process if needed.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		_ = c.stdin.Close()
		wait := make(chan error, 1)
		go func() { wait <- c.cmd.Wait() }()
		select {
		case werr := <-wait:
			err = werr
		case <-time.After(3 * time.Second):
			c.kill()
			<-wait
		}
	})
	return err
}

func (c *Client) kill() {
	if c.cmd != nil && c.cmd.Process != nil {
		killProcessGroup(c.cmd.Process)
	}
}

// Handshake starts an app-server, initializes and immediately closes it. It is
// used by doctor to verify the local protocol works.
func Handshake(ctx context.Context, opts Options) (*InitializeResult, error) {
	c, err := Start(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if c.Init == nil {
		return nil, errors.New("no initialize result")
	}
	cp := *c.Init
	return &cp, nil
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
