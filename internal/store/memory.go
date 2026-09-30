package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- Models ----

type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

type Workspace struct {
	ID        string
	OwnerID   string
	Name      string
	CreatedAt time.Time
}

type Document struct {
	ID          string
	WorkspaceID string
	Filename    string
	SHA256      string
	ChunkCount  int
	CreatedAt   time.Time
}

// Chunk lives in ONE shared store for all workspaces; WorkspaceID is the tenant tag.
type Chunk struct {
	ID          string
	WorkspaceID string
	DocumentID  string
	Filename    string
	Index       int
	Content     string
	Embedding   []float32
	Score       float64 // filled on search
}

type Message struct {
	ID          string
	WorkspaceID string
	Role        string // user | assistant
	Content     string
	Citations   string // JSON-ish display string
	LatencyMs   int64
	Tokens      int
	Hit         bool
	CreatedAt   time.Time
}

type Task struct {
	ID          string
	WorkspaceID string
	Title       string
	Notes       string
	CreatedAt   time.Time
}

type ToolCall struct {
	ID          string
	WorkspaceID string
	Name        string
	Args        string
	OK          bool
	Result      string
	CreatedAt   time.Time
}

// ---- Store interface ----

type Store interface {
	CreateUser(ctx context.Context, email, hash string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	CreateSession(ctx context.Context, userID string) (string, error)
	GetSessionUser(ctx context.Context, token string) (*User, error)
	DeleteSession(ctx context.Context, token string) error

	CreateWorkspace(ctx context.Context, ownerID, name string) (*Workspace, error)
	ListWorkspaces(ctx context.Context, ownerID string) ([]*Workspace, error)
	GetWorkspace(ctx context.Context, id, ownerID string) (*Workspace, error)

	CreateDocument(ctx context.Context, wsID, filename, sha string, nchunks int) (*Document, error)
	DocExists(ctx context.Context, wsID, sha string) bool
	ListDocuments(ctx context.Context, wsID string) ([]*Document, error)

	AddChunks(ctx context.Context, chunks []Chunk) error
	// SearchChunks MUST filter workspace_id inside the query (SQL WHERE) — never post-filter.
	SearchChunks(ctx context.Context, wsID string, q []float32, topK int) ([]Chunk, error)
	ChunkCount(ctx context.Context) (int, error)

	AddMessage(ctx context.Context, m Message) (*Message, error)
	ListMessages(ctx context.Context, wsID string, limit int) ([]*Message, error)

	CreateTask(ctx context.Context, wsID, title, notes string) (*Task, error)
	ListTasks(ctx context.Context, wsID string) ([]*Task, error)

	LogTool(ctx context.Context, t ToolCall) error
	ListTools(ctx context.Context, wsID string, limit int) ([]*ToolCall, error)
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func HashSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Cosine similarity for the in-memory fallback.
func Cosine(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ---- MemoryStore: used when DATABASE_URL is unset (local dev) ----
// Same semantics: single shared slice, search filters workspace first.

type MemoryStore struct {
	mu       sync.RWMutex
	users    map[string]*User
	sessions map[string]string // token -> userID
	ws       map[string]*Workspace
	docs     map[string]*Document
	chunks   []Chunk // SHARED store
	msgs     []Message
	tasks    []Task
	tools    []ToolCall
}

func NewMemory() *MemoryStore {
	return &MemoryStore{
		users:    map[string]*User{},
		sessions: map[string]string{},
		ws:       map[string]*Workspace{},
		docs:     map[string]*Document{},
	}
}

func (m *MemoryStore) CreateUser(ctx context.Context, email, hash string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Email, email) {
			return nil, fmt.Errorf("email taken")
		}
	}
	u := &User{ID: NewID(), Email: email, PasswordHash: hash, CreatedAt: time.Now()}
	m.users[u.ID] = u
	return u, nil
}

func (m *MemoryStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *MemoryStore) CreateSession(ctx context.Context, userID string) (string, error) {
	t := NewID() + NewID()
	m.mu.Lock()
	m.sessions[t] = userID
	m.mu.Unlock()
	return t, nil
}

func (m *MemoryStore) GetSessionUser(ctx context.Context, token string) (*User, error) {
	m.mu.RLock()
	uid, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no session")
	}
	m.mu.RLock()
	u := m.users[uid]
	m.mu.RUnlock()
	if u == nil {
		return nil, fmt.Errorf("no user")
	}
	return u, nil
}

func (m *MemoryStore) DeleteSession(ctx context.Context, token string) error {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) CreateWorkspace(ctx context.Context, ownerID, name string) (*Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := &Workspace{ID: NewID()[:12], OwnerID: ownerID, Name: name, CreatedAt: time.Now()}
	m.ws[w.ID] = w
	return w, nil
}

func (m *MemoryStore) ListWorkspaces(ctx context.Context, ownerID string) ([]*Workspace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Workspace
	for _, w := range m.ws {
		if w.OwnerID == ownerID {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) GetWorkspace(ctx context.Context, id, ownerID string) (*Workspace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w, ok := m.ws[id]
	if !ok || w.OwnerID != ownerID {
		return nil, fmt.Errorf("not found")
	}
	return w, nil
}

func (m *MemoryStore) CreateDocument(ctx context.Context, wsID, filename, sha string, n int) (*Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := &Document{ID: NewID()[:12], WorkspaceID: wsID, Filename: filename, SHA256: sha, ChunkCount: n, CreatedAt: time.Now()}
	m.docs[d.ID] = d
	return d, nil
}

func (m *MemoryStore) DocExists(ctx context.Context, wsID, sha string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, d := range m.docs {
		if d.WorkspaceID == wsID && d.SHA256 == sha {
			return true
		}
	}
	return false
}

func (m *MemoryStore) ListDocuments(ctx context.Context, wsID string) ([]*Document, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Document
	for _, d := range m.docs {
		if d.WorkspaceID == wsID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) AddChunks(ctx context.Context, cs []Chunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range cs {
		if cs[i].ID == "" {
			cs[i].ID = NewID()
		}
		m.chunks = append(m.chunks, cs[i])
	}
	return nil
}

func (m *MemoryStore) SearchChunks(ctx context.Context, wsID string, q []float32, topK int) ([]Chunk, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Workspace filter FIRST (tenant boundary), then rank — mirrors SQL WHERE.
	var scoped []Chunk
	for _, c := range m.chunks {
		if c.WorkspaceID == wsID {
			c.Score = Cosine(q, c.Embedding)
			scoped = append(scoped, c)
		}
	}
	sort.Slice(scoped, func(i, j int) bool { return scoped[i].Score > scoped[j].Score })
	if len(scoped) > topK {
		scoped = scoped[:topK]
	}
	return scoped, nil
}

func (m *MemoryStore) ChunkCount(ctx context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.chunks), nil
}

func (m *MemoryStore) AddMessage(ctx context.Context, x Message) (*Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x.ID = NewID()[:12]
	x.CreatedAt = time.Now()
	m.msgs = append(m.msgs, x)
	return &x, nil
}

func (m *MemoryStore) ListMessages(ctx context.Context, wsID string, limit int) ([]*Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Message
	for i := len(m.msgs) - 1; i >= 0 && len(out) < limit; i-- {
		if m.msgs[i].WorkspaceID == wsID {
			cp := m.msgs[i]
			out = append(out, &cp)
		}
	}
	// chronological
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (m *MemoryStore) CreateTask(ctx context.Context, wsID, title, notes string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := Task{ID: NewID()[:12], WorkspaceID: wsID, Title: title, Notes: notes, CreatedAt: time.Now()}
	m.tasks = append(m.tasks, t)
	return &t, nil
}

func (m *MemoryStore) ListTasks(ctx context.Context, wsID string) ([]*Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Task
	for i := range m.tasks {
		if m.tasks[i].WorkspaceID == wsID {
			cp := m.tasks[i]
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemoryStore) LogTool(ctx context.Context, t ToolCall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.ID = NewID()[:12]
	t.CreatedAt = time.Now()
	m.tools = append(m.tools, t)
	return nil
}

func (m *MemoryStore) ListTools(ctx context.Context, wsID string, limit int) ([]*ToolCall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*ToolCall
	for i := len(m.tools) - 1; i >= 0 && len(out) < limit; i-- {
		if m.tools[i].WorkspaceID == wsID {
			cp := m.tools[i]
			out = append(out, &cp)
		}
	}
	return out, nil
}
