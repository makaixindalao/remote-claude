package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 会话记录的缓存。读一次要把整个会话文件解析一遍：几十 MB 的 claude 会话要零点几秒，
// 几百 MB 的 codex 会话要好几秒。页面只要最后一页，往上翻、展开工具卡片时再要别的部分，
// 所以解析结果按文件的 (mtime, size) 留在内存里，翻页、取工具全文都直接从这里切。
// 会话文件一追加，key 就变了，旧的那份自然被挤出去。
//
// 另外给每个响应算 ETag：不读文件就知道浏览器手里那份是不是最新的，是就回 304。

const (
	sessionLimit    = 3000     // 太长的会话只留最后这么多条
	sessionCacheMax = 96 << 20 // LRU 里的总大小（估算）
)

// 换了程序（解析逻辑可能变了）旧 ETag 一律作废
var bootID = strconv.FormatInt(time.Now().UnixNano(), 36)

type sessionCache struct {
	mu       sync.Mutex
	order    list.List // 最近用过的在前，元素是 *sessionData
	items    map[string]*list.Element
	size     int
	inflight map[string]*sessionLoad // 同一个会话同时被要，只解析一次
}

type sessionData struct {
	key     string
	entries []Entry // 只读：从缓存里拿出去的，谁都不许改
	base    int     // entries[0] 在整个会话里是第几条：前面太老的没留（最多留 sessionLimit 条）
	size    int
}

type sessionLoad struct {
	done chan struct{}
	data *sessionData
	err  error
}

func (c *sessionCache) get(key string) *sessionData {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(el)
	return el.Value.(*sessionData)
}

func (c *sessionCache) put(d *sessionData) {
	if d.size > sessionCacheMax/3 {
		return // 一份就占掉一大块的不存，免得把别的全挤走
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*list.Element{}
	}
	if _, ok := c.items[d.key]; ok {
		return
	}
	c.items[d.key] = c.order.PushFront(d)
	c.size += d.size
	for c.size > sessionCacheMax {
		old := c.order.Remove(c.order.Back()).(*sessionData)
		delete(c.items, old.key)
		c.size -= old.size
	}
}

// sessionFile：决定会话内容的那个文件（grok 是会话目录下的 updates.jsonl）
func sessionFile(info SessionInfo) string {
	if info.Agent == agentGrok {
		return filepath.Join(info.path, "updates.jsonl")
	}
	return info.path
}

// sessionKey：stat 一下会话文件，内容变了 key 就变。stat 不了返回空串（不缓存）
func sessionKey(info SessionInfo) string {
	file := sessionFile(info)
	fi, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", info.Agent, file, fi.ModTime().UnixNano(), fi.Size())
}

// loadSession：会话的 Entry（最后 sessionLimit 条），优先走缓存
func (s *Server) loadSession(info SessionInfo) (*sessionData, error) {
	key := sessionKey(info)
	if key == "" {
		return parseSession(info)
	}
	c := &s.sessCache
	if d := c.get(key); d != nil {
		return d, nil
	}
	c.mu.Lock()
	if l, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-l.done
		return l.data, l.err
	}
	if c.inflight == nil {
		c.inflight = map[string]*sessionLoad{}
	}
	l := &sessionLoad{done: make(chan struct{})}
	c.inflight[key] = l
	c.mu.Unlock()

	l.data, l.err = parseSession(info)
	l.data.key = key
	if l.err == nil {
		c.put(l.data)
	}
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	close(l.done)
	return l.data, l.err
}

// parseSession 读全部再只留最后 sessionLimit 条。下标从会话开头算（base），文件往后追加时
// 已经发给页面的下标不会错位；留下的那段复制出来，前面的整块才能被回收
func parseSession(info SessionInfo) (*sessionData, error) {
	all, _, err := sessionEntries(info, 0)
	base := max(0, len(all)-sessionLimit)
	entries := append([]Entry(nil), all[base:]...)
	return &sessionData{entries: entries, base: base, size: entriesSize(entries)}, err
}

// entriesSize 粗估占用：字符串本身加上结构体的零头
func entriesSize(entries []Entry) int {
	n := 0
	for _, e := range entries {
		n += 64 + len(e.TS)
		for _, b := range e.Blocks {
			n += 96 + len(b.Text) + len(b.ID) + len(b.Name) + len(b.Input)
		}
	}
	return n
}

// etagOf：内容由这些东西决定的响应的 ETag
func etagOf(parts ...any) string {
	h := sha256.New()
	fmt.Fprint(h, bootID)
	enc := json.NewEncoder(h)
	for _, p := range parts {
		enc.Encode(p)
	}
	return `"` + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:15]) + `"`
}

func etagMatch(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		if t = strings.TrimSpace(t); t == etag || t == "W/"+etag || t == "*" {
			return true
		}
	}
	return false
}
