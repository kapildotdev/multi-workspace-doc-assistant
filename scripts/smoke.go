package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"workspace-assistant/internal/llm"
	"workspace-assistant/internal/rag"
	"workspace-assistant/internal/store"
	"workspace-assistant/internal/tools"
)

func main() {
	ctx := context.Background()
	st := store.NewMemory()
	lc := llm.New("", "x", "y")
	fail := 0
	ok := func(name string, cond bool) {
		if cond {
			fmt.Println("PASS", name)
		} else {
			fmt.Println("FAIL", name)
			fail++
		}
	}

	u, _ := st.CreateUser(ctx, "test@example.com", "h")
	wA, _ := st.CreateWorkspace(ctx, u.ID, "Workspace A")
	wB, _ := st.CreateWorkspace(ctx, u.ID, "Workspace B")

	ingest := func(wsID, fn, text string) {
		for i, ch := range rag.ChunkText(text, 800, 150) {
			e, _ := lc.Embed(ctx, ch)
			_ = st.AddChunks(ctx, []store.Chunk{{WorkspaceID: wsID, DocumentID: fn, Filename: fn, Index: i, Content: ch, Embedding: e}})
		}
		_, _ = st.CreateDocument(ctx, wsID, fn, store.HashSHA(wsID+fn), len(rag.ChunkText(text, 800, 150)))
	}
	ingest(wA.ID, "helios.txt", "Project Helios launch token is ZX-99. Only Workspace A knows this.")
	ingest(wB.ID, "other.txt", "Workspace B discusses gardening and soil pH.")

	// isolation: search B for A-fact
	qe, _ := lc.Embed(ctx, "What is the Helios token ZX-99?")
	hitsB, _ := st.SearchChunks(ctx, wB.ID, qe, 5)
	leak := false
	for _, h := range hitsB {
		if strings.Contains(h.Content, "ZX-99") {
			leak = true
		}
	}
	ok("isolation: B search never returns A fact", !leak)
	hitsA, _ := st.SearchChunks(ctx, wA.ID, qe, 5)
	found := false
	for _, h := range hitsA {
		if strings.Contains(h.Content, "ZX-99") {
			found = true
		}
	}
	ok("retrieval: A search finds its fact", found)

	// idempotency signal
	exists := st.DocExists(ctx, wA.ID, store.HashSHA(wA.ID+"helios.txt"))
	ok("idempotent doc hash recognized", exists)

	// tool validation
	_, _, err := tools.ValidateSaveTask(map[string]any{})
	ok("save_task rejects missing title", err != nil)
	_, _, err = tools.ValidateSaveTask(map[string]any{"title": "x"})
	ok("save_task accepts valid", err == nil)
	_, err = tools.ValidateSendSummary(map[string]any{})
	ok("send_summary rejects empty", err != nil)

	// unknown tool must be rejected (simulated via stub chat + handler logic is in web; check ToolDefs allowlist)
	allowed := map[string]bool{}
	for _, t := range llm.ToolDefs() {
		allowed[t.Name] = true
	}
	ok("allowlist has exactly save_task+send_summary", allowed["save_task"] && allowed["send_summary"] && len(allowed) == 2)
	ok("unknown tool delete_everything not allowlisted", !allowed["delete_everything"])

	// honest refusal: empty workspace
	wC, _ := st.CreateWorkspace(ctx, u.ID, "Empty")
	qe2, _ := lc.Embed(ctx, "totally unrelated xyzzy question")
	hitsC, _ := st.SearchChunks(ctx, wC.ID, qe2, 5)
	ok("empty workspace returns no chunks", len(hitsC) == 0)
	res := lc.Chat
	_ = res
	r, _ := lc.Chat(ctx, "what is helios?", "(no chunks)", "")
	ok("stub says don't know on empty context", strings.Contains(strings.ToLower(r.Text), "don't know"))

	// heal: empty doc shell (failed 404-era upload) is detected and removable
	shell, _ := st.CreateDocument(ctx, wC.ID, "shell.txt", "deadbeef", 3)
	n, _ := st.CountChunksForDoc(ctx, shell.ID)
	ok("empty shell detected (0 chunks)", n == 0)
	_ = st.DeleteDocument(ctx, shell.ID)
	docs, _ := st.ListDocuments(ctx, wC.ID)
	gone := true
	for _, d := range docs {
		if d.ID == shell.ID {
			gone = false
		}
	}
	ok("empty shell deleted so re-upload heals", gone)

	if fail > 0 {
		os.Exit(1)
	}
	fmt.Println("ALL SMOKE CHECKS PASSED")
}
