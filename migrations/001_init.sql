-- Single shared vector store for ALL workspaces. Tenancy = workspace_id column.
create extension if not exists pgvector with schema extensions;
create table if not exists users(id text primary key, email text unique not null, password_hash text not null, created_at timestamptz default now());
create table if not exists sessions(token text primary key, user_id text references users(id) on delete cascade, created_at timestamptz default now());
create table if not exists workspaces(id text primary key, owner_id text references users(id) on delete cascade, name text not null, created_at timestamptz default now());
create table if not exists documents(id text primary key, workspace_id text not null, filename text not null, sha256 text not null, chunk_count int default 0, created_at timestamptz default now(), unique(workspace_id, sha256));
create table if not exists chunks(id text primary key, workspace_id text not null, document_id text not null, filename text not null default '', chunk_index int default 0, content text not null, embedding extensions.vector(768) not null, created_at timestamptz default now());
create index if not exists idx_chunks_ws on chunks(workspace_id);
create index if not exists idx_chunks_emb on chunks using ivfflat (embedding extensions.vector_cosine_ops) with (lists=50);
create table if not exists messages(id text primary key, workspace_id text not null, role text not null, content text not null, citations text default '', latency_ms bigint default 0, tokens int default 0, hit boolean default false, created_at timestamptz default now());
create table if not exists tasks(id text primary key, workspace_id text not null, title text not null, notes text default '', created_at timestamptz default now());
create table if not exists tool_calls(id text primary key, workspace_id text not null, name text not null, args text default '', ok boolean default false, result text default '', created_at timestamptz default now());
-- Canonical workspace-scoped search: filter INSIDE the vector query.
-- select document_id, filename, chunk_index, content, 1 - (embedding <=> $1::extensions.vector) as score from chunks where workspace_id=$2 order by embedding <=> $1::extensions.vector limit $3;
