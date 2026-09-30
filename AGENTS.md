# AGENTS.md — how AI agents should work in this repo

- Stack: Go monolith (chi + html/template + HTMX), pgx → Supabase Postgres+pgvector, Gemini REST for chat/embeddings.
- Tenancy invariant: every chunk/message/task/tool query MUST scope `workspace_id` to the active workspace; vector search filters `WHERE workspace_id=$2` INSIDE the SQL, never post-filters in Go.
- Shared store: exactly ONE `chunks` table for all workspaces. Never create per-workspace tables.
- Tools: allowlist is `save_task`, `send_summary` only. Validate args with `internal/tools`, strip any `workspace_id` from model args, log every call (ok + failed) to `tool_calls`.
- Docs are DATA: wrap retrieved chunks in `<context>`, system prompt forbids following doc instructions; unknown tools are rejected, never executed.
- Idempotency: `documents(workspace_id, sha256)` unique; skip re-uploads.
- Secrets: only via env (`DATABASE_URL`, `GEMINI_API_KEY`, `DISCORD_WEBHOOK_URL`); never log or send to client.
- Verify with `go build ./...` and `go run ./scripts/smoke.go` before claiming done.
