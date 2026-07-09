Why did test_match.go succeed but the real test failed with `no such column: k`?
Let me check where `Vector Search Error: sqlite3: SQL logic error: no such column: k` is coming from.
Ah, is there another Search call for Events maybe?
B3: `RunSearchMemory` in `pkg/mcp/mcp.go`. Does it use `vStore.Search`?
Wait! `RunSearchMemory` does a loop over `events` table because it doesn't use `VectorStore`!
Is `vStore.Search` only called in `RunSearch`?
Wait, if it's `RunSearchMemory`, I didn't touch it.

Let's check where `no such column: k` occurs.
In `pkg/vector/vector.go:74`: `fmt.Printf("Vector Search Error: %v\n", err)`
This means it IS from `vStore.Search`.
Wait! Is it possible that the query uses `k = ?2` but `k` is a reserved column only available when `vec_items` has `embedding float[xxx]`...
Wait! If it is `vec0(embedding float[768])`, does it have `k`? Yes, test_match used `float[3]` and it worked.
But wait! `pkg/db/db.go` was NOT creating `vectors` with `float[768]` in some code paths?
Ah! `pkg/db/db.go` has `CREATE VIRTUAL TABLE IF NOT EXISTS vectors USING vec0(embedding float[768]);`
Is it possible the database was already created with the old schema in the e2e test, and it's reusing an old `test.db`?
Yes! E2E CLI tests might reuse `m_cpg.db` from disk because we just run `m-cpg-go-e2e-test search SQLite`.
Wait, in `main_test.go`:
```go
func TestE2ECLI(t *testing.T) { ...
```
Does `TestE2ECLI` use a fresh database?
