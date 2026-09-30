package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"workspace-assistant/internal/config"
	"workspace-assistant/internal/llm"
	"workspace-assistant/internal/rag"
	"workspace-assistant/internal/store"
	"workspace-assistant/internal/tools"
)

type Server struct {
	Store store.Store
	LLM   *llm.Client
	Cfg   config.Config
	Tpl   *template.Template
}

func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.handleLanding)
	r.Get("/login", s.handleLoginPage)
	r.Post("/login", s.handleLogin)
	r.Get("/signup", s.handleSignupPage)
	r.Post("/signup", s.handleSignup)
	r.Post("/logout", s.handleLogout)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/app", s.handleApp)
		r.Post("/workspaces", s.handleCreateWS)
		r.Get("/switch", s.handleSwitch)
		r.Post("/upload", s.handleUpload)
		r.Post("/chat", s.handleChat)
		r.Get("/debug", s.handleDebug)
	})
	return r
}

type ctxKey string

const userKey ctxKey = "user"

func (s *Server) currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		u, err := s.Store.GetSessionUser(r.Context(), c.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (s *Server) activeWS(r *http.Request, u *store.User) *store.Workspace {
	wsID := r.URL.Query().Get("ws")
	if wsID == "" {
		if c, err := r.Cookie("ws"); err == nil {
			wsID = c.Value
		}
	}
	if wsID != "" {
		if w, err := s.Store.GetWorkspace(r.Context(), wsID, u.ID); err == nil {
			return w
		}
	}
	ws, _ := s.Store.ListWorkspaces(r.Context(), u.ID)
	if len(ws) > 0 {
		return ws[0]
	}
	return nil
}

func setWSCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: "ws", Value: id, Path: "/", MaxAge: 86400 * 30, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// ---- pages ----

func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		if _, err := s.Store.GetSessionUser(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/app", http.StatusSeeOther)
			return
		}
	}
	s.Tpl.ExecuteTemplate(w, "landing", nil)
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.Tpl.ExecuteTemplate(w, "login", map[string]any{"Error": r.URL.Query().Get("e")})
}
func (s *Server) handleSignupPage(w http.ResponseWriter, r *http.Request) {
	s.Tpl.ExecuteTemplate(w, "signup", map[string]any{"Error": r.URL.Query().Get("e")})
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	pw := r.FormValue("password")
	if email == "" || len(pw) < 6 {
		http.Redirect(w, r, "/signup?e=need+valid+email+and+6%2B+char+password", http.StatusSeeOther)
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	u, err := s.Store.CreateUser(r.Context(), email, string(h))
	if err != nil {
		http.Redirect(w, r, "/signup?e=email+taken", http.StatusSeeOther)
		return
	}
	tok, _ := s.Store.CreateSession(r.Context(), u.ID)
	http.SetCookie(w, &http.Cookie{Name: "session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	// seed two workspaces for first-timers
	w1, _ := s.Store.CreateWorkspace(r.Context(), u.ID, "Workspace A")
	w2, _ := s.Store.CreateWorkspace(r.Context(), u.ID, "Workspace B")
	_ = w1
	_ = w2
	if w1 != nil {
		setWSCookie(w, w1.ID)
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	pw := r.FormValue("password")
	u, err := s.Store.GetUserByEmail(r.Context(), email)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pw)) != nil {
		http.Redirect(w, r, "/login?e=invalid+credentials", http.StatusSeeOther)
		return
	}
	tok, _ := s.Store.CreateSession(r.Context(), u.ID)
	http.SetCookie(w, &http.Cookie{Name: "session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type AppData struct {
	User      *store.User
	Workspaces []*store.Workspace
	Active    *store.Workspace
	Docs      []*store.Document
	Messages  []*store.Message
	Tasks     []*store.Task
	ToolCalls []*store.ToolCall
	Debug     []store.Chunk
	Notice    string
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	wsList, _ := s.Store.ListWorkspaces(r.Context(), u.ID)
	active := s.activeWS(r, u)
	if active == nil {
		s.Tpl.ExecuteTemplate(w, "app", &AppData{User: u, Workspaces: wsList, Notice: r.URL.Query().Get("n")})
		return
	}
	setWSCookie(w, active.ID)
	docs, _ := s.Store.ListDocuments(r.Context(), active.ID)
	msgs, _ := s.Store.ListMessages(r.Context(), active.ID, 50)
	tasks, _ := s.Store.ListTasks(r.Context(), active.ID)
	tcalls, _ := s.Store.ListTools(r.Context(), active.ID, 50)
	s.Tpl.ExecuteTemplate(w, "app", &AppData{
		User: u, Workspaces: wsList, Active: active, Docs: docs,
		Messages: msgs, Tasks: tasks, ToolCalls: tcalls, Notice: r.URL.Query().Get("n"),
	})
}

func (s *Server) handleCreateWS(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = fmt.Sprintf("Workspace %d", time.Now().Unix()%10000)
	}
	nw, err := s.Store.CreateWorkspace(r.Context(), u.ID, name)
	if err == nil {
		setWSCookie(w, nw.ID)
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	id := r.URL.Query().Get("id")
	if _, err := s.Store.GetWorkspace(r.Context(), id, u.ID); err == nil {
		setWSCookie(w, id)
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// ---- upload ----

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	active := s.activeWS(r, u)
	if active == nil {
		http.Redirect(w, r, "/app?n=create+a+workspace+first", http.StatusSeeOther)
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Redirect(w, r, "/app?n=upload+too+large+%28max+10MB%29", http.StatusSeeOther)
		return
	}
	files := r.MultipartForm.File["docs"]
	if len(files) == 0 {
		// also support single "doc"
		files = r.MultipartForm.File["doc"]
	}
	count, skipped, failed := 0, 0, 0
	existingDocs, _ := s.Store.ListDocuments(r.Context(), active.ID)
	findBySHA := func(sha string) *store.Document {
		for _, d := range existingDocs {
			if d.SHA256 == sha {
				return d
			}
		}
		return nil
	}
	// healable duplicate: a doc row whose chunks never embedded (e.g. past API
	// outage) is deleted so the re-upload actually ingests instead of skipping.
	ensureFresh := func(sha string) bool {
		if d := findBySHA(sha); d != nil {
			if n, _ := s.Store.CountChunksForDoc(r.Context(), d.ID); n > 0 {
				skipped++
				return false // true idempotent duplicate
			}
			_ = s.Store.DeleteDocument(r.Context(), d.ID)
		}
		return true
	}
	ingestChunks := func(filename, sha string, texts []string) {
		var cs []store.Chunk
		docID := store.NewID()
		for i, ch := range texts {
			emb, err := s.LLM.Embed(r.Context(), ch)
			if err != nil {
				log.Printf("upload embed failed for %s chunk %d: %v", filename, i, err)
				continue
			}
			cs = append(cs, store.Chunk{ID: store.NewID(), WorkspaceID: active.ID, DocumentID: docID, Filename: filename, Index: i, Content: ch, Embedding: emb})
		}
		if len(cs) == 0 {
			failed++
			return
		}
		if _, err := s.Store.CreateDocument(r.Context(), active.ID, filename, sha, len(cs)); err != nil {
			// raced duplicate: still try to store chunks under existing doc? skip.
			failed++
			return
		}
		// point chunks at the real doc id: look it up (CreateDocument made its own id)
		if d := func() *store.Document {
			docs, _ := s.Store.ListDocuments(r.Context(), active.ID)
			for _, x := range docs {
				if x.SHA256 == sha {
					return x
				}
			}
			return nil
		}(); d != nil {
			for i := range cs {
				cs[i].DocumentID = d.ID
			}
		}
		if err := s.Store.AddChunks(r.Context(), cs); err != nil {
			log.Printf("upload AddChunks failed for %s: %v", filename, err)
			failed++
			return
		}
		count++
	}
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			failed++
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, 5<<20))
		f.Close()
		if err != nil {
			failed++
			continue
		}
		text := string(raw)
		// naive PDF guard: reject binary PDFs with clear message
		if strings.HasSuffix(strings.ToLower(fh.Filename), ".pdf") && strings.Contains(text, "%PDF") && len(extractText(text)) < 50 {
			failed++
			continue
		}
		h := sha256.Sum256(raw)
		sha := hex.EncodeToString(h[:])
		if !ensureFresh(sha) {
			continue
		}
		chunks := rag.ChunkText(extractText(text), 800, 150)
		if len(chunks) == 0 {
			failed++
			continue
		}
		ingestChunks(fh.Filename, sha, chunks)
	}
	// also support pasted text
	if pasted := strings.TrimSpace(r.FormValue("pastetext")); pasted != "" {
		name := strings.TrimSpace(r.FormValue("pastename"))
		if name == "" {
			name = "pasted.txt"
		}
		sha := store.HashSHA(active.ID + name + pasted)
		if ensureFresh(sha) {
			if chunks := rag.ChunkText(pasted, 800, 150); len(chunks) > 0 {
				ingestChunks(name, sha, chunks)
			} else {
				failed++
			}
		}
	}
	notice := fmt.Sprintf("ingested+%d+document%%28s%%29,+skipped+%d+duplicate%%28s%%29,+failed+%d", count, skipped, failed)
	http.Redirect(w, r, "/app?n="+notice, http.StatusSeeOther)
}

func extractText(s string) string {
	// strip NULs and excessive binary; keep printable
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > 200000 {
		s = s[:200000]
	}
	return strings.TrimSpace(s)
}

// ---- chat ----

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	active := s.activeWS(r, u)
	if active == nil {
		http.Redirect(w, r, "/app?n=create+a+workspace+first", http.StatusSeeOther)
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		http.Redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	_, _ = s.Store.AddMessage(ctx, store.Message{WorkspaceID: active.ID, Role: "user", Content: q})

	qemb, err := s.LLM.Embed(ctx, q)
	if err != nil {
		log.Printf("embed failed (question saved, ask to retry): %v", err)
		_, _ = s.Store.AddMessage(ctx, store.Message{WorkspaceID: active.ID, Role: "assistant",
			Content: "Retrieval failed temporarily — your question is saved, please retry.", Hit: false, LatencyMs: time.Since(t0).Milliseconds()})
		http.Redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	// Workspace-scoped vector search (filter inside query).
	hits, err := s.Store.SearchChunks(ctx, active.ID, qemb, 8)
	if err != nil {
		hits = nil
	}
	hit := hasSupport(q, hits)
	var fnames, contents []string
	var idx []int
	for _, h := range hits {
		if h.Score < 0.02 {
			continue
		}
		fnames = append(fnames, h.Filename)
		contents = append(contents, h.Content)
		idx = append(idx, h.Index)
	}
	contextBlock := rag.BuildContext(fnames, contents, idx)
	if !hit {
		contextBlock = "(no chunks)"
	}
	hist, _ := s.Store.ListMessages(ctx, active.ID, 6)
	var hb strings.Builder
	for _, m := range hist {
		hb.WriteString(m.Role + ": " + truncate(m.Content, 300) + "\n")
	}
	res, err := s.LLM.Chat(ctx, q, contextBlock, hb.String())
	if err != nil {
		log.Printf("chat failed (question saved, ask to retry): %v", err)
		_, _ = s.Store.AddMessage(ctx, store.Message{WorkspaceID: active.ID, Role: "assistant",
			Content: "The model call failed — your question is saved, please retry.", Hit: hit, LatencyMs: time.Since(t0).Milliseconds()})
		http.Redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	// Tool loop (max 2 rounds): validate → execute → log → feed back.
	var toolNotes []string
	var finalText = res.Text
	for round := 0; round < 2 && len(res.ToolCalls) > 0; round++ {
		var results []string
		for _, tc := range res.ToolCalls {
			out, ok := s.execTool(ctx, active.ID, tc.Name, tc.Args)
			results = append(results, tc.Name+": "+out)
			toolNotes = append(toolNotes, tc.Name)
			_ = ok
		}
		// second pass: let model incorporate results (or stub synthesis)
		if s.LLM.HasKey() {
			follow, err := s.LLM.Chat(ctx, q+" [tool results: "+strings.Join(results, " | ")+"]", contextBlock, hb.String())
			if err == nil {
				finalText = follow.Text
				if len(follow.ToolCalls) > 0 && round == 0 {
					res.ToolCalls = follow.ToolCalls
					continue
				}
			}
		} else if finalText == "" || strings.HasPrefix(finalText, "Based on") || strings.HasPrefix(finalText, "I don't know") {
			finalText = synthesizeStub(q, contextBlock, toolNotes, results)
		}
		break
	}
	if strings.TrimSpace(finalText) == "" {
		finalText = synthesizeStub(q, contextBlock, toolNotes, nil)
	}
	if !hit && !strings.Contains(strings.ToLower(finalText), "don't know") && len(toolNotes) == 0 {
		// enforce honest refusal when no support (unless a tool answered)
		finalText = "I don't know — this workspace's documents don't contain the answer."
	}
	cites := ""
	if hit {
		for i := range fnames {
			if i >= 4 {
				break
			}
			if cites != "" {
				cites += ", "
			}
			cites += fmt.Sprintf("[%s §%d]", fnames[i], idx[i])
		}
	}
	if IsRefusal(finalText) {
		// The model's own honest refusal is authoritative: a "don't know"
		// must never carry citations nor count as a retrieval hit — including
		// citations the model baked into its own prose.
		finalText = StripCitations(finalText)
		cites = ""
		hit = false
	}
	_, _ = s.Store.AddMessage(ctx, store.Message{
		WorkspaceID: active.ID, Role: "assistant", Content: finalText, Citations: cites,
		LatencyMs: time.Since(t0).Milliseconds(), Tokens: res.Tokens, Hit: hit,
	})
	_ = u
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (s *Server) execTool(ctx context.Context, wsID, name string, args map[string]any) (string, bool) {
	if args == nil {
		args = map[string]any{}
	}
	// strip any workspace_id the model tried to smuggle (tenancy guard)
	delete(args, "workspace_id")
	delete(args, "workspaceId")
	argStr := fmt.Sprintf("%v", args)
	switch name {
	case "save_task":
		title, notes, err := tools.ValidateSaveTask(args)
		if err != nil {
			_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: false, Result: err.Error()})
			return "validation failed: " + err.Error(), false
		}
		t, err := s.Store.CreateTask(ctx, wsID, title, notes)
		if err != nil {
			_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: false, Result: err.Error()})
			return "failed: " + err.Error(), false
		}
		_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: true, Result: "saved task " + t.ID})
		return "saved task \"" + title + "\"", true
	case "send_summary":
		text, err := tools.ValidateSendSummary(args)
		if err != nil {
			_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: false, Result: err.Error()})
			return "validation failed: " + err.Error(), false
		}
		out, err := tools.SendSummary(ctx, s.Cfg.WebhookURL, text)
		if err != nil {
			_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: false, Result: err.Error()})
			return "failed: " + err.Error(), false
		}
		_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: true, Result: out})
		return out, true
	default:
		// unknown tool: never execute, log failure (prompt-injection guard)
		_ = s.Store.LogTool(ctx, store.ToolCall{WorkspaceID: wsID, Name: name, Args: argStr, OK: false, Result: "unknown tool rejected"})
		return "rejected: unknown tool \"" + name + "\"", false
	}
}

// hasSupport: hit if vector score reasonable OR clear keyword overlap with scoped chunks.
func hasSupport(q string, hits []store.Chunk) bool {
	if len(hits) == 0 {
		return false
	}
	if hits[0].Score > 0.12 {
		return true
	}
	qtoks := keywords(q)
	if len(qtoks) == 0 {
		return false
	}
	for _, h := range hits[:minN(len(hits), 3)] {
		cl := strings.ToLower(h.Content)
		overlap := 0
		for _, t := range qtoks {
			if len(t) >= 4 && strings.Contains(cl, t) {
				overlap++
			}
		}
		if overlap >= 2 || (overlap >= 1 && h.Score > 0.02) {
			return true
		}
	}
	return false
}

func keywords(s string) []string {
	stop := map[string]bool{"what": true, "when": true, "where": true, "which": true, "who": true, "does": true, "do": true, "is": true, "are": true, "the": true, "a": true, "an": true, "in": true, "of": true, "for": true, "to": true, "tell": true, "me": true, "about": true, "and": true, "it": true}
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, "?,.!;:\"'()")
		if len(w) < 3 || stop[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

func synthesizeStub(q, contextBlock string, toolNotes, results []string) string {
	var b strings.Builder
	if contextBlock != "" && contextBlock != "(no chunks)" {
		b.WriteString("Based on this workspace's documents:\n\n" + contextBlock)
	} else {
		b.WriteString("I don't know — this workspace's documents don't contain the answer.")
	}
	if len(toolNotes) > 0 {
		b.WriteString("\n\nTools used: " + strings.Join(toolNotes, ", "))
		if len(results) > 0 {
			b.WriteString(" — " + strings.Join(results, " | "))
		}
	}
	_ = q
	return b.String()
}

// IsRefusal reports whether the answer text is an honest "don't know".
func IsRefusal(s string) bool {
	l := strings.ToLower(s)
	for _, p := range []string{
		"don't know", "do not know", "dont know",
		"no mention", "not mention", "does not contain", "don't contain",
		"not in the provided context", "not contain the answer",
		"no information", "cannot answer", "can't answer",
	} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// StripCitations removes "[file §N]" markers from refusal text.
var citeRe = regexp.MustCompile(`\[[^\[\]]+§\d+\]`)

func StripCitations(s string) string {
	out := citeRe.ReplaceAllString(s, "")
	out = strings.Join(strings.Fields(out), " ")
	out = strings.ReplaceAll(out, " .", ".")
	out = strings.ReplaceAll(out, " ,", ",")
	return strings.TrimSpace(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func minN(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// ---- debug ----

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	active := s.activeWS(r, u)
	if active == nil {
		http.Redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		q = "test query"
	}
	qemb, _ := s.LLM.Embed(r.Context(), q)
	hits, _ := s.Store.SearchChunks(r.Context(), active.ID, qemb, 8)
	type row struct {
		Score float64
		File  string
		Index int
		Snip  string
	}
	var rows []row
	for _, h := range hits {
		rows = append(rows, row{h.Score, h.Filename, h.Index, truncate(h.Content, 160)})
	}
	total, _ := s.Store.ChunkCount(r.Context())
	s.Tpl.ExecuteTemplate(w, "debug", map[string]any{
		"Active": active, "Q": q, "Rows": rows, "Total": total,
		"Workspaces": mustWS(s, r, u),
	})
}

func mustWS(s *Server, r *http.Request, u *store.User) []*store.Workspace {
	ws, _ := s.Store.ListWorkspaces(r.Context(), u.ID)
	return ws
}
