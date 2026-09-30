package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store on Supabase/any Postgres+pgvector.
// All chunk reads filter workspace_id INSIDE the SQL vector query.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, url string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	s := &PostgresStore{pool: pool}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	// Best-effort: Supabase pre-installs pgvector; non-superusers can't CREATE EXTENSION.
	bestEffort := []string{
		`create extension if not exists vector with schema extensions`,
		`create extension if not exists pgcrypto`,
	}
	for _, q := range bestEffort {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			fmt.Println("note: extension step skipped:", err)
		}
	}
	required := []string{
		`create table if not exists users(id text primary key, email text unique not null, password_hash text not null, created_at timestamptz default now())`,
		`create table if not exists sessions(token text primary key, user_id text references users(id) on delete cascade, created_at timestamptz default now())`,
		`create table if not exists workspaces(id text primary key, owner_id text references users(id) on delete cascade, name text not null, created_at timestamptz default now())`,
		`create table if not exists documents(id text primary key, workspace_id text not null, filename text not null, sha256 text not null, chunk_count int default 0, created_at timestamptz default now(), unique(workspace_id, sha256))`,
		// SINGLE shared vector store for every workspace.
		`create table if not exists chunks(id text primary key, workspace_id text not null, document_id text not null, filename text not null default '', chunk_index int default 0, content text not null, embedding extensions.vector(768) not null, created_at timestamptz default now())`,
		`create table if not exists messages(id text primary key, workspace_id text not null, role text not null, content text not null, citations text default '', latency_ms bigint default 0, tokens int default 0, hit boolean default false, created_at timestamptz default now())`,
		`create table if not exists tasks(id text primary key, workspace_id text not null, title text not null, notes text default '', created_at timestamptz default now())`,
		`create table if not exists tool_calls(id text primary key, workspace_id text not null, name text not null, args text default '', ok boolean default false, result text default '', created_at timestamptz default now())`,
		`create index if not exists idx_chunks_ws on chunks(workspace_id)`,
	}
	for _, q := range required {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("migrate %q: %w", q, err)
		}
	}
	// ANN index (best-effort; ivfflat needs enough rows anyway)
	_, _ = s.pool.Exec(ctx, `create index if not exists idx_chunks_emb on chunks using ivfflat (embedding extensions.vector_cosine_ops) with (lists=50)`)
	return nil
}

func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%f", f)
	}
	b.WriteByte(']')
	return b.String()
}

func (s *PostgresStore) CreateUser(ctx context.Context, email, hash string) (*User, error) {
	u := &User{ID: NewID(), Email: strings.ToLower(email), PasswordHash: hash}
	_, err := s.pool.Exec(ctx, `insert into users(id,email,password_hash) values($1,$2,$3)`, u.ID, u.Email, hash)
	return u, err
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx, `select id,email,password_hash from users where email=$1`, strings.ToLower(email)).Scan(&u.ID, &u.Email, &u.PasswordHash)
	return u, err
}

func (s *PostgresStore) CreateSession(ctx context.Context, userID string) (string, error) {
	t := NewID() + NewID()
	_, err := s.pool.Exec(ctx, `insert into sessions(token,user_id) values($1,$2)`, t, userID)
	return t, err
}

func (s *PostgresStore) GetSessionUser(ctx context.Context, token string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx, `select u.id,u.email,u.password_hash from users u join sessions s on s.user_id=u.id where s.token=$1`, token).Scan(&u.ID, &u.Email, &u.PasswordHash)
	return u, err
}

func (s *PostgresStore) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where token=$1`, token)
	return err
}

func (s *PostgresStore) CreateWorkspace(ctx context.Context, ownerID, name string) (*Workspace, error) {
	w := &Workspace{ID: NewID()[:12], OwnerID: ownerID, Name: name}
	_, err := s.pool.Exec(ctx, `insert into workspaces(id,owner_id,name) values($1,$2,$3)`, w.ID, ownerID, name)
	return w, err
}

func (s *PostgresStore) ListWorkspaces(ctx context.Context, ownerID string) ([]*Workspace, error) {
	rows, err := s.pool.Query(ctx, `select id,owner_id,name from workspaces where owner_id=$1 order by created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workspace
	for rows.Next() {
		w := &Workspace{}
		if err := rows.Scan(&w.ID, &w.OwnerID, &w.Name); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetWorkspace(ctx context.Context, id, ownerID string) (*Workspace, error) {
	w := &Workspace{}
	err := s.pool.QueryRow(ctx, `select id,owner_id,name from workspaces where id=$1 and owner_id=$2`, id, ownerID).Scan(&w.ID, &w.OwnerID, &w.Name)
	return w, err
}

func (s *PostgresStore) CreateDocument(ctx context.Context, wsID, filename, sha string, n int) (*Document, error) {
	d := &Document{ID: NewID()[:12], WorkspaceID: wsID, Filename: filename, SHA256: sha, ChunkCount: n}
	_, err := s.pool.Exec(ctx, `insert into documents(id,workspace_id,filename,sha256,chunk_count) values($1,$2,$3,$4,$5)`, d.ID, wsID, filename, sha, n)
	return d, err
}

func (s *PostgresStore) DocExists(ctx context.Context, wsID, sha string) bool {
	var n int
	_ = s.pool.QueryRow(ctx, `select count(*) from documents where workspace_id=$1 and sha256=$2`, wsID, sha).Scan(&n)
	return n > 0
}

func (s *PostgresStore) ListDocuments(ctx context.Context, wsID string) ([]*Document, error) {
	rows, err := s.pool.Query(ctx, `select id,workspace_id,filename,sha256,chunk_count from documents where workspace_id=$1 order by created_at desc`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Document
	for rows.Next() {
		d := &Document{}
		if err := rows.Scan(&d.ID, &d.WorkspaceID, &d.Filename, &d.SHA256, &d.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CountChunksForDoc(ctx context.Context, docID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `select count(*) from chunks where document_id=$1`, docID).Scan(&n)
	return n, err
}

func (s *PostgresStore) DeleteDocument(ctx context.Context, docID string) error {
	_, err := s.pool.Exec(ctx, `delete from chunks where document_id=$1`, docID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `delete from documents where id=$1`, docID)
	return err
}

func (s *PostgresStore) AddChunks(ctx context.Context, cs []Chunk) error {
	batch := &pgx.Batch{}
	for _, c := range cs {
		id := c.ID
		if id == "" {
			id = NewID()
		}
		batch.Queue(`insert into chunks(id,workspace_id,document_id,filename,chunk_index,content,embedding) values($1,$2,$3,$4,$5,$6,$7::extensions.vector)`,
			id, c.WorkspaceID, c.DocumentID, c.Filename, c.Index, c.Content, vecLiteral(c.Embedding))
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range cs {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// SearchChunks: workspace filter is part of the vector query (tenancy boundary).
func (s *PostgresStore) SearchChunks(ctx context.Context, wsID string, q []float32, topK int) ([]Chunk, error) {
	rows, err := s.pool.Query(ctx, `
		select document_id, filename, chunk_index, content, 1 - (embedding OPERATOR(extensions.<=>) $1::extensions.vector) as score
		from chunks
		where workspace_id = $2
		order by embedding OPERATOR(extensions.<=>) $1::extensions.vector
		limit $3`, vecLiteral(q), wsID, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		c.WorkspaceID = wsID
		if err := rows.Scan(&c.DocumentID, &c.Filename, &c.Index, &c.Content, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ChunkCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `select count(*) from chunks`).Scan(&n)
	return n, err
}

func (s *PostgresStore) AddMessage(ctx context.Context, x Message) (*Message, error) {
	x.ID = NewID()[:12]
	_, err := s.pool.Exec(ctx, `insert into messages(id,workspace_id,role,content,citations,latency_ms,tokens,hit) values($1,$2,$3,$4,$5,$6,$7,$8)`,
		x.ID, x.WorkspaceID, x.Role, x.Content, x.Citations, x.LatencyMs, x.Tokens, x.Hit)
	return &x, err
}

func (s *PostgresStore) ListMessages(ctx context.Context, wsID string, limit int) ([]*Message, error) {
	rows, err := s.pool.Query(ctx, `select id,workspace_id,role,content,citations,latency_ms,tokens,hit from messages where workspace_id=$1 order by created_at desc limit $2`, wsID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		x := &Message{}
		if err := rows.Scan(&x.ID, &x.WorkspaceID, &x.Role, &x.Content, &x.Citations, &x.LatencyMs, &x.Tokens, &x.Hit); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	// chronological
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateTask(ctx context.Context, wsID, title, notes string) (*Task, error) {
	t := &Task{ID: NewID()[:12], WorkspaceID: wsID, Title: title, Notes: notes}
	_, err := s.pool.Exec(ctx, `insert into tasks(id,workspace_id,title,notes) values($1,$2,$3,$4)`, t.ID, wsID, title, notes)
	return t, err
}

func (s *PostgresStore) ListTasks(ctx context.Context, wsID string) ([]*Task, error) {
	rows, err := s.pool.Query(ctx, `select id,workspace_id,title,notes from tasks where workspace_id=$1 order by created_at desc`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t := &Task{}
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Title, &t.Notes); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) LogTool(ctx context.Context, t ToolCall) error {
	t.ID = NewID()[:12]
	_, err := s.pool.Exec(ctx, `insert into tool_calls(id,workspace_id,name,args,ok,result) values($1,$2,$3,$4,$5,$6)`,
		t.ID, t.WorkspaceID, t.Name, t.Args, t.OK, t.Result)
	return err
}

func (s *PostgresStore) ListTools(ctx context.Context, wsID string, limit int) ([]*ToolCall, error) {
	rows, err := s.pool.Query(ctx, `select id,workspace_id,name,args,ok,result from tool_calls where workspace_id=$1 order by created_at desc limit $2`, wsID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ToolCall
	for rows.Next() {
		t := &ToolCall{}
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Args, &t.OK, &t.Result); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
