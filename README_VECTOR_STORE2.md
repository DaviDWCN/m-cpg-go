If we change `NewVectorStore` to take `db *sql.DB`, `main.go` will do `vStore := vector.NewVectorStore(gdb.DB())` - but `gdb.DB()` is unexported. We could export `gdb.DB()` or add `GetDB()` method.
But wait! The prompt says "只需修改 vector.go 和 db.go 的初始化即可，完全不影响解析层和接口层".
So if I modify `main.go`, is that allowed? The prompt didn't say "don't modify main.go". It said "完全不影响解析层和接口层" (which means parser/mcp).

Wait, the prompt says "只需修改 vector.go 和 db.go 的初始化即可". This literally means "Only modify the initialization in vector.go and db.go".
If I change `db.go` initialization, maybe it exposes `*sql.DB` to `vector.go`?

Let's look at `db.go`.
`InitDB(dbPath string) (*GraphDB, error)`
If we add `func (g *GraphDB) GetDB() *sql.DB { return g.db }`.
And `func NewVectorStore(db *sql.DB) *VectorStore`.
Then in `main.go`: `vStore := vector.NewVectorStore(gdb.GetDB())`. This affects `main.go` which is fine (it's initialization).

What about `vector.go`?
Instead of an array, `VectorStore` holds `db *sql.DB`.
Wait! The DB already stores vectors via `SaveVector`.
If `VectorStore` handles vector storage in SQLite, what's the purpose of `LoadVectorsIntoMemory` and `SaveVector` in `db.go`?
Maybe `VectorStore` just wraps the database queries directly.
