package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 按行分隔的 JSON-RPC 2.0：codex app-server 和 grok 的 ACP 都是这个形状，只是 codex 不带 "jsonrpc" 字段。
//
// 这里只管请求 ID 和应答的配对；对方发来的请求（审批）和通知交还给调用方处理。
// 注意对方的请求 ID 可能是 0（grok 就从 0 数），所以用原文判断有没有 ID，不能看数值。

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type rpcResult struct {
	data json.RawMessage
	err  error
}

type rpcConn struct {
	write   func(v any) error
	done    <-chan struct{} // 进程退出时关闭：等应答的一律放弃
	version bool            // 发出去的消息带不带 "jsonrpc":"2.0"
	name    string          // 报错时用：「codex 没有响应」

	n     atomic.Int64
	mu    sync.Mutex
	calls map[string]chan rpcResult
}

func newRPC(name string, write func(v any) error, done <-chan struct{}, version bool) *rpcConn {
	return &rpcConn{write: write, done: done, version: version, name: name, calls: map[string]chan rpcResult{}}
}

func (r *rpcConn) msg(m rpcMsg) rpcMsg {
	if r.version {
		m.JSONRPC = "2.0"
	}
	return m
}

// start 发请求、不等应答；拆出来是为了让调用方先把请求写出去，再决定在哪儿等
func (r *rpcConn) start(method string, params any) (chan rpcResult, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := strconv.FormatInt(r.n.Add(1), 10)
	ch := make(chan rpcResult, 1)
	r.mu.Lock()
	r.calls[id] = ch
	r.mu.Unlock()
	if err := r.write(r.msg(rpcMsg{ID: json.RawMessage(id), Method: method, Params: raw})); err != nil {
		r.mu.Lock()
		delete(r.calls, id)
		r.mu.Unlock()
		return nil, err
	}
	return ch, nil
}

// wait 等 start 的应答；timeout 为 0 表示一直等（grok 的 session/prompt 要等整轮跑完）
func (r *rpcConn) wait(ch chan rpcResult, timeout time.Duration) (json.RawMessage, error) {
	var after <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		after = t.C
	}
	select {
	case res := <-ch:
		return res.data, res.err
	case <-after:
		return nil, errors.New(r.name + " 没有响应")
	case <-r.done:
		return nil, errors.New(r.name + " 进程已经不在了")
	}
}

func (r *rpcConn) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	ch, err := r.start(method, params)
	if err != nil {
		return nil, err
	}
	return r.wait(ch, timeout)
}

func (r *rpcConn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return r.write(r.msg(rpcMsg{Method: method, Params: raw}))
}

func (r *rpcConn) reply(id json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return r.write(r.msg(rpcMsg{ID: id, Result: raw}))
}

func (r *rpcConn) replyErr(id json.RawMessage, code int, message string) error {
	return r.write(r.msg(rpcMsg{ID: id, Error: &rpcError{Code: code, Message: message}}))
}

// dispatch 处理 stdout 的一行：是应答就交给等它的调用方，返回 nil；
// 是对方的请求（有 ID 有 method）或通知（只有 method）就原样返回给调用方
func (r *rpcConn) dispatch(line []byte) *rpcMsg {
	var m rpcMsg
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	if m.Method != "" {
		return &m
	}
	if !hasID {
		return nil
	}
	r.mu.Lock()
	ch := r.calls[string(m.ID)]
	delete(r.calls, string(m.ID))
	r.mu.Unlock()
	if ch != nil {
		res := rpcResult{data: m.Result}
		if m.Error != nil {
			res.err = fmt.Errorf("%s 拒绝了: %s", r.name, m.Error.Message)
		}
		ch <- res
	}
	return nil
}

func (m *rpcMsg) isRequest() bool { return len(m.ID) > 0 && string(m.ID) != "null" }
