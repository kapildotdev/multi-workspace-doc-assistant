# AI_NOTES.md

## Tools & split of work

- Coding agent (this session, Muse Spark via OpenCode) scaffolded the whole repo: Go monolith, store abstraction (pgx + in-memory), Gemini REST client, chunking, tool validation, HTML dashboard, smoke checks, README.
- I (developer) set direction: Go + Postgres stack, Supabase for vectors, "pick hosting for me", "don't compromise on assessment quality". I reviewed the isolation/tool contracts and test outputs rather than hand-writing each handler.

## Key decisions I made

1. **Go server-rendered monolith over Go API + React.** One binary serves UI + API, so Render free needs a single web service and evaluators get a working URL faster. HTMX-level interactivity (plain forms) is enough for the rubric; no SPA build step to break.
2. **Workspace filter inside the vector SQL, mirrored in the memory fallback.** `WHERE workspace_id=$2 ... ORDER BY embedding <=> $1` is the tenancy boundary — post-filtering in Go would leak cross-tenant scores and violate the brief. The in-memory store filters first, then ranks, so offline behavior matches Postgres semantics.
3. **Deterministic offline fallbacks (hash embeddings + extractive stub chat).** Guarantees the app is gradable with zero keys: retrieval, isolation, idempotency, and both tools still function. Real Gemini lights up automatically when `GEMINI_API_KEY` is set — no code change, no second path to maintain.

## Hardest bug / wrong turn (honest)

The agent's first embedding normalizer was broken math: it tried to hand-roll an L2 norm with a Newton loop wrapped in confusing scaling (`/ (s * ... * 0 + 1)`), which was both wrong-looking and fragile. I noticed because `go run ./scripts/smoke.go` isolation scores were near-zero/unstable in review and the function was unreadable. Fix: replace with a plain, obviously-correct L2 normalize (`v[i] /= sqrt(sum)`) — I had the agent simplify rather than patch the clever version. Lesson: for security-adjacent numeric code (retrieval ranking), prefer boring and auditable over clever; verify with the smoke test that A-finds-its-fact while B-doesn't-leak.

## With more time

- Hybrid search (tsvector keyword + vector, reciprocal-rank fusion) with the workspace filter pushed into both legs; re-rank top-20 with a cross-encoder.
- Streaming assistant tokens over SSE + per-request token/latency dashboard charts.
- Supabase Auth + RLS as defense-in-depth on top of app-level scoping; background ingestion worker with retries.
