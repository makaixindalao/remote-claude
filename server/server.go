package main

import (
	"encoding/json"
	"net/http"
	"sync"
)

type Server struct {
	cfg        *Config
	auth       *Auth
	chats      *ChatManager
	metas      metaCache
	catalogs   catalogCache
	archive    *archiveStore
	settings   *settingsStore
	push       *pushStore
	reclaudeMu sync.Mutex // 见 runReclaude
	codex      codexIndex
	grok       grokIndex

	sessCache sessionCache
}

func newServer(cfg *Config) *Server {
	s := &Server{
		cfg:      cfg,
		auth:     newAuth(cfg.Password, cfg.Secret),
		chats:    newChatManager(cfg),
		metas:    metaCache{m: map[string]cachedMeta{}},
		catalogs: catalogCache{items: map[string]catalogItem{}},
		archive:  newArchiveStore(stateDir()),
		settings: newSettingsStore(stateDir()),
		push:     newPushStore(stateDir()),
	}
	s.chats.onCatalog = s.storeCatalog
	s.chats.onPrompt = s.appendHistory
	s.chats.claudeBin = s.claudeBin
	s.chats.onNotice = s.notify
	go reapUploads(uploadDir(), uploadTTL)
	return s
}

func (s *Server) routes() http.Handler {
	a := s.auth
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("POST /api/logout", a.handleLogout)
	mux.HandleFunc("GET /api/me", a.require(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "root": s.cfg.Root, "agents": s.availableAgents()})
	}))
	mux.HandleFunc("GET /api/projects", a.require(s.handleProjects))
	mux.HandleFunc("GET /api/sessions", a.require(s.handleSessions))
	mux.HandleFunc("GET /api/session", a.require(s.handleSession))
	mux.HandleFunc("GET /api/session/tools", a.require(s.handleSessionTools))
	mux.HandleFunc("GET /api/tmux", a.require(s.handleTmuxList))
	mux.HandleFunc("GET /api/catalog", a.require(s.handleCatalog))
	mux.HandleFunc("GET /api/file", a.require(s.handleFile))
	mux.HandleFunc("GET /api/diff", a.require(s.handleDiff))
	mux.HandleFunc("GET /api/diff/file", a.require(s.handleDiffFile))
	mux.HandleFunc("GET /api/limits", a.require(s.handleLimits))
	mux.HandleFunc("GET /api/history", a.require(s.handleHistory))
	mux.HandleFunc("GET /api/reclaude", a.require(s.handleReclaude))
	mux.HandleFunc("POST /api/reclaude/enabled", a.require(s.handleReclaudeEnabled))
	mux.HandleFunc("GET /api/reclaude/status", a.require(s.handleReclaudeStatus))
	mux.HandleFunc("GET /api/reclaude/orgs", a.require(s.handleReclaudeOrgs))
	mux.HandleFunc("GET /api/reclaude/gateway", a.require(s.handleReclaudeGateway))
	mux.HandleFunc("POST /api/reclaude/org", a.require(s.handleReclaudeOrg))
	mux.HandleFunc("POST /api/reclaude/gateway", a.require(s.handleReclaudeGatewaySet))
	mux.HandleFunc("POST /api/reclaude/gateway/test", a.require(s.handleReclaudeGatewayTest))
	mux.HandleFunc("POST /api/reclaude/gateway/reset", a.require(s.handleReclaudeGatewayReset))
	mux.HandleFunc("GET /api/notify", a.require(s.handleNotify))
	mux.HandleFunc("POST /api/notify", a.require(s.handleNotifySave))
	mux.HandleFunc("POST /api/notify/test", a.require(s.handleNotifyTest))
	mux.HandleFunc("POST /api/push/subscribe", a.require(s.handlePushSubscribe))
	mux.HandleFunc("POST /api/push/unsubscribe", a.require(s.handlePushUnsubscribe))
	mux.HandleFunc("GET /api/prefs", a.require(s.handlePrefs))
	mux.HandleFunc("POST /api/prefs", a.require(s.handlePrefsSave))
	mux.HandleFunc("GET /api/rc", a.require(s.handleRC))
	mux.HandleFunc("POST /api/rc", a.require(s.handleRCSave))
	mux.HandleFunc("POST /api/rc/restart", a.require(s.handleRCRestart))
	mux.HandleFunc("POST /api/session/archive", a.require(s.handleArchive))
	mux.HandleFunc("POST /api/session/delete", a.require(s.handleDeleteSession))
	mux.HandleFunc("POST /api/uploads", a.require(s.handleUpload))
	mux.HandleFunc("GET /api/uploads/{id}", a.require(s.handleUploadFile))
	mux.HandleFunc("GET /api/uploads/{id}/preview", a.require(s.handleUploadFile))
	mux.HandleFunc("DELETE /api/uploads/{id}", a.require(s.handleUploadDelete))
	mux.HandleFunc("GET /api/chats", a.require(s.handleChats))
	mux.HandleFunc("POST /api/chats", a.require(s.handleStartChat))
	mux.HandleFunc("DELETE /api/chats/{id}", a.require(s.handleCloseChat))
	mux.HandleFunc("GET /api/chats/{id}/entries", a.require(s.handleChatEntries))
	mux.HandleFunc("GET /api/chats/{id}/tools", a.require(s.handleChatTools))
	mux.HandleFunc("GET /ws/chats/{id}", a.require(s.handleChatWS))
	mux.HandleFunc("GET /ws/term", a.require(s.handleTermWS))

	// 图标、manifest、service worker 都不用登录：浏览器取它们时不一定带 cookie
	for p := range iconSizes {
		mux.HandleFunc("GET "+p, handleIcon)
	}
	mux.Handle("GET /", newStatic())
	return securityHeaders(gzipAPI(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// blob:：输入框里附件的缩略图、给模型缩图时解码原图，用的都是本地文件的 object URL（见 ui 的 lib/attachments.ts）
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https:; "+
			"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
