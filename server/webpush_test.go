package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// RFC 8291 附录 A 的例子：固定的密钥和 salt，加密结果逐字节对得上
func TestEncryptPushRFC8291(t *testing.T) {
	dec := func(s string) []byte {
		b, err := b64.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	as, err := ecdh.P256().NewPrivateKey(dec("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(as.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("as_public = %s", got)
	}
	body, err := encryptPush(
		[]byte("When I grow up, I want to be a watermelon"),
		dec("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		dec("BTBZMqHH6r4Tts7J_aSIgg"),
		as,
		dec("DGv6ra1nlYgDCS1FRnbzlw"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64.EncodeToString(body); got != want {
		t.Fatalf("加密结果对不上\n got %s\nwant %s", got, want)
	}
}

func TestVapidJWT(t *testing.T) {
	k, _ := ecdh.P256().NewPrivateKey(make32(7))
	priv, pub, err := vapidKey(k.Bytes())
	if err != nil || len(pub) != 65 || pub[0] != 4 {
		t.Fatalf("key: %v %d", err, len(pub))
	}
	jwt, err := vapidJWT(priv, "https://fcm.googleapis.com", "mailto:a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %s", jwt)
	}
	sig, _ := b64.DecodeString(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if len(sig) != 64 || !ecdsa.Verify(&priv.PublicKey, h[:], r, s) {
		t.Fatal("签名验不过")
	}
}

func make32(b byte) []byte {
	out := make([]byte, 32)
	out[31] = b
	return out
}

// send 的整条路：本地 https 服务当推送服务，按浏览器那边的钥匙解开正文；410 之后订阅被删掉
func TestPushSendRoundTrip(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var got []byte
	var hdr http.Header
	gone := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		got = decryptPush(t, body, ua, auth)
		if gone {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	old := pushClient
	pushClient = srv.Client()
	defer func() { pushClient = old }()

	ps := newPushStore(t.TempDir())
	sub := pushSub{Endpoint: srv.URL + "/push/abc", P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString(auth)}
	if err := ps.add(sub); err != nil {
		t.Fatal(err)
	}
	if err := ps.send(sub, []byte(`{"title":"hi"}`), "high", "chat1"); err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"title":"hi"}` {
		t.Fatalf("解出来是 %q", got)
	}
	if hdr.Get("Content-Encoding") != "aes128gcm" || hdr.Get("Urgency") != "high" || hdr.Get("Topic") != "chat1" || hdr.Get("TTL") == "" {
		t.Fatalf("header: %v", hdr)
	}
	key, _ := ps.publicKey()
	if a := hdr.Get("Authorization"); !strings.HasPrefix(a, "vapid t=") || !strings.HasSuffix(a, ", k="+key) {
		t.Fatalf("Authorization: %s", a)
	}

	gone = true
	if err := ps.send(sub, []byte("x"), "normal", ""); !errors.Is(err, errPushGone) || ps.has(sub.Endpoint) {
		t.Fatalf("410 之后应该删掉订阅: %v", err)
	}
	// 密钥存下来了，换个 store 读回来是同一把
	if key2, _ := newPushStore(filepath.Dir(ps.path)).publicKey(); key2 != key {
		t.Fatal("VAPID 密钥没存住")
	}
}

func decryptPush(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, idlen := body[:16], int(body[20])
	asPub := body[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := ua.ECDH(as)
	ikm, _ := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatalf("解不开: %v", err)
	}
	return plain[:len(plain)-1]
}
