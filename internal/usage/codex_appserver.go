package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// codexRPCMessage covers every line shape Codex's app-server JSON-RPC
// protocol can send: a call's response (ID set, no Method), or a
// notification/request from the server (Method set). This client never
// issues a turn, so it expects no server->client requests in practice and
// simply drops any Method-bearing line it receives (see read).
type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// codexAppServer is a minimal JSON-RPC-over-stdio client for `codex
// app-server --stdio`, just enough to read account/rateLimits/read (see
// codex.go). It does not implement approvals, turns, or any other part of
// the protocol maatgen's fuller adapter supports — this package never runs
// a turn, so none of that is reachable.
type codexAppServer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	nextID atomic.Int64

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan codexRPCMessage

	done    chan struct{}
	waitErr error
}

func startCodexAppServer(ctx context.Context, binary, dir string) (*codexAppServer, error) {
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	cmd.Dir = dir
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	c := &codexAppServer{
		cmd: cmd, stdin: stdin,
		pending: map[int64]chan codexRPCMessage{},
		done:    make(chan struct{}),
	}
	go c.read(stdout)
	go func() {
		c.waitErr = cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

// call sends a JSON-RPC request and blocks for its matching response (by
// id), ctx cancellation, or the app-server process exiting, whichever comes
// first.
func (c *codexAppServer) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan codexRPCMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if c.waitErr != nil {
			return fmt.Errorf("codex app-server exited: %w", c.waitErr)
		}
		return errors.New("codex app-server exited before responding")
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("%s: %s (code %d)", method, msg.Error.Message, msg.Error.Code)
		}
		if result != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	}
}

func (c *codexAppServer) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *codexAppServer) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *codexAppServer) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var msg codexRPCMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method != "" || len(msg.ID) == 0 {
			// Server->client notification or request: this client never starts a
			// turn, so nothing it does should provoke one (e.g. an approval
			// request); safe to drop.
			continue
		}
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[id]
		c.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

// close terminates the app-server process and waits for it to exit. Safe to
// call even if the handshake never completed.
func (c *codexAppServer) close() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	<-c.done
}
