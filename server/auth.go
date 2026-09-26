package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 登录模型：密码换一个 HttpOnly cookie，cookie 里只有「过期时间 + HMAC」，服务端不存会话。
// HMAC 的 key 由密码（和可选的 RCWEB_SECRET）派生，所以改密码 = 所有已登录的浏览器一起失效，
// 而重启 / 重新部署不会把人踢下线。

const (
	cookieName = "rcweb_session"
	sessionTTL = 30 * 24 * time.Hour

	// 失败次数按来源 IP 计：直接开到公网时，别人乱试密码不会把你也锁在外面。
	// 经 Tailscale / SSH 隧道进来的请求 RemoteAddr 都是 127.0.0.1，那时就等于全局计数
	maxFailures   = 10
	failureWindow = 10 * time.Minute
)

type Auth struct {
	key    []byte
	pwHash [32]byte

	mu       sync.Mutex
	failures map[string][]time.Time // 来源 IP → 窗口内每次失败的时间
}

func newAuth(password, secret string) *Auth {
	h := sha256.New()
	h.Write([]byte("rcweb-cookie\x00" + password + "\x00" + secret))
	return &Auth{key: h.Sum(nil), pwHash: sha256.Sum256([]byte(password)), failures: map[string][]time.Time{}}
}

func (a *Auth) token(exp int64) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) validToken(v string) bool {
	expStr, _, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(a.token(exp)), []byte(v))
}

func (a *Auth) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && a.validToken(c.Value)
}

// clientIP 只看 TCP 对端：直连公网时 X-Forwarded-For 谁都能伪造。
// IPv6 按 /64 算，一个用户通常握有整段地址，按单个地址限等于没限
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return host
}

// locked 报告 ip 是否处于失败过多的冷却期，顺手清掉所有 IP 窗口外的旧记录
func (a *Auth) locked(ip string) (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-failureWindow)
	for k, ts := range a.failures {
		i := 0
		for i < len(ts) && ts[i].Before(cut) {
			i++
		}
		if i == len(ts) {
			delete(a.failures, k)
		} else {
			a.failures[k] = ts[i:]
		}
	}
	if ts := a.failures[ip]; len(ts) >= maxFailures {
		return true, time.Until(ts[0].Add(failureWindow))
	}
	return false, 0
}

func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if locked, wait := a.locked(ip); locked {
		writeErr(w, http.StatusTooManyRequests, "密码错误次数过多，请 "+wait.Round(time.Second).String()+" 后再试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	got := sha256.Sum256([]byte(body.Password))
	if subtle.ConstantTimeCompare(got[:], a.pwHash[:]) != 1 {
		a.mu.Lock()
		a.failures[ip] = append(a.failures[ip], time.Now())
		a.mu.Unlock()
		time.Sleep(time.Second)
		writeErr(w, http.StatusUnauthorized, "密码不对")
		return
	}
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    a.token(exp.Unix()),
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// require 挡住没登录的请求；会改状态的请求和 WebSocket 额外校验 Origin，
// 防止别的网页借浏览器里的 cookie 发请求（跨站 WebSocket 劫持 / CSRF）。
func (a *Auth) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authed(r) {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		if (r.Method != http.MethodGet || isWebSocket(r)) && !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "来源校验失败")
			return
		}
		next(w, r)
	}
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// sameOrigin：Origin 的 host 要等于请求的 Host。反向代理（tailscale serve 等）可能改写 Host，
// 所以 X-Forwarded-Host 也认 —— 跨站页面想自己带这个头得先过 CORS 预检，而本服务从不应答预检；
// WebSocket 握手则根本不让页面脚本设请求头。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host || (r.Header.Get("X-Forwarded-Host") != "" && u.Host == r.Header.Get("X-Forwarded-Host"))
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
