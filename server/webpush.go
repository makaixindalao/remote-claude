package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Web Push：浏览器（手机上的 Chrome、加到主屏幕的 Safari、电脑浏览器）订阅后，页面关了、手机锁屏也能收到通知。
// 服务端用 VAPID（RFC 8292）签名、按 RFC 8291 加密，发给浏览器厂商的推送服务（FCM / Mozilla / Apple）。
// 只有 https 或 localhost 打开的页面能订阅，这是浏览器的规矩。
//
// VAPID 密钥和订阅存在 $RCWEB_STATE_DIR/push.json。换了密钥，已有的订阅就都失效了，所以只生成一次。

type pushSub struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"` // 浏览器的公钥，base64url
	Auth     string `json:"auth"`   // 16 字节的 auth secret，base64url
	Origin   string `json:"origin,omitempty"`
	Created  int64  `json:"created"`
}

type pushStore struct {
	mu   sync.Mutex
	path string
	data struct {
		Key  string    `json:"key"` // VAPID 私钥（P-256 的 d），base64url
		Subs []pushSub `json:"subs"`
	}
	priv *ecdsa.PrivateKey
	pub  []byte // 未压缩的公钥，65 字节
}

var b64 = base64.RawURLEncoding

func newPushStore(dir string) *pushStore {
	ps := &pushStore{path: filepath.Join(dir, "push.json")}
	if data, err := os.ReadFile(ps.path); err == nil {
		_ = json.Unmarshal(data, &ps.data)
	}
	return ps
}

// keyLocked：VAPID 密钥，没有就生成一对存下来
func (ps *pushStore) keyLocked() error {
	if ps.priv != nil {
		return nil
	}
	if ps.data.Key != "" {
		if d, err := b64.DecodeString(ps.data.Key); err == nil {
			if priv, pub, err := vapidKey(d); err == nil {
				ps.priv, ps.pub = priv, pub
				return nil
			}
		}
	}
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	priv, pub, err := vapidKey(k.Bytes())
	if err != nil {
		return err
	}
	ps.data.Key = b64.EncodeToString(k.Bytes())
	if err := ps.saveLocked(); err != nil {
		return err
	}
	ps.priv, ps.pub = priv, pub
	return nil
}

// vapidKey：32 字节的 d → 签名用的 ecdsa 私钥 + 未压缩公钥
func vapidKey(d []byte) (*ecdsa.PrivateKey, []byte, error) {
	k, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, nil, err
	}
	pub := k.PublicKey().Bytes()
	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pub[1:33]), Y: new(big.Int).SetBytes(pub[33:])},
		D:         new(big.Int).SetBytes(d),
	}
	return priv, pub, nil
}

func (ps *pushStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(ps.path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(ps.data, "", "  ")
	tmp := ps.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ps.path)
}

// publicKey：给页面订阅用（applicationServerKey）
func (ps *pushStore) publicKey() (string, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if err := ps.keyLocked(); err != nil {
		return "", err
	}
	return b64.EncodeToString(ps.pub), nil
}

func (ps *pushStore) list() []pushSub {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]pushSub(nil), ps.data.Subs...)
}

func (ps *pushStore) has(endpoint string) bool {
	for _, s := range ps.list() {
		if s.Endpoint == endpoint {
			return true
		}
	}
	return false
}

// add：同一个 endpoint 再订阅一次就换成新的钥匙
func (ps *pushStore) add(sub pushSub) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	subs := []pushSub{sub}
	for _, s := range ps.data.Subs {
		if s.Endpoint != sub.Endpoint {
			subs = append(subs, s)
		}
	}
	ps.data.Subs = subs
	return ps.saveLocked()
}

func (ps *pushStore) remove(endpoint string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	subs := []pushSub{}
	for _, s := range ps.data.Subs {
		if s.Endpoint != endpoint {
			subs = append(subs, s)
		}
	}
	ps.data.Subs = subs
	return ps.saveLocked()
}

var errPushGone = errors.New("这个浏览器的订阅已经失效")

var pushClient = &http.Client{Timeout: 20 * time.Second}

// send 推一条消息。推送服务说订阅没了（404 / 410）就删掉它，返回 errPushGone
func (ps *pushStore) send(sub pushSub, payload []byte, urgency, topic string) error {
	ps.mu.Lock()
	err := ps.keyLocked()
	priv, pub := ps.priv, ps.pub
	ps.mu.Unlock()
	if err != nil {
		return err
	}
	uaPub, err := b64.DecodeString(strings.TrimRight(sub.P256dh, "="))
	if err != nil {
		return fmt.Errorf("订阅的公钥不对: %w", err)
	}
	auth, err := b64.DecodeString(strings.TrimRight(sub.Auth, "="))
	if err != nil {
		return fmt.Errorf("订阅的 auth 不对: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	body, err := encryptPush(payload, uaPub, auth, as, salt)
	if err != nil {
		return err
	}
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" {
		return errors.New("订阅地址不对")
	}
	jwt, err := vapidJWT(priv, u.Scheme+"://"+u.Host, vapidSubject(sub.Origin))
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", urgency)
	if topic != "" {
		req.Header.Set("Topic", topic) // 同一个对话还没送达的旧通知被新的顶掉
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+b64.EncodeToString(pub))
	resp, err := pushClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		_ = ps.remove(sub.Endpoint)
		return errPushGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("推送服务返回 %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// vapidSubject：联系方式。Apple 要求是 mailto: 或 https:，用订阅时页面的 https 地址，没有就给个 mailto
func vapidSubject(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return origin
	}
	return "mailto:rcweb@example.com"
}

// vapidJWT：ES256 签名的 JWT，aud 是推送服务的 origin，12 小时有效
func vapidJWT(priv *ecdsa.PrivateKey, aud, sub string) (string, error) {
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": sub})
	input := header + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, priv, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64.EncodeToString(sig), nil
}

// encryptPush：RFC 8291（aes128gcm）。as 是这一条消息临时生成的密钥对，salt 16 字节随机
func encryptPush(plaintext, uaPub, auth []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("订阅的公钥不对: %w", err)
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	keyInfo := "WebPush: info\x00" + string(uaPub) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, secret, auth, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// 只有一个记录：正文后面跟 0x02（最后一个记录的分隔符），不加填充
	sealed := gcm.Seal(nil, nonce, append(append([]byte(nil), plaintext...), 2), nil)
	// 头：salt(16) | rs(4) | idlen(1) | keyid = 临时公钥(65)
	out := make([]byte, 0, 16+4+1+len(asPub)+len(sealed))
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return append(out, sealed...), nil
}
