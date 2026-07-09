Since we are using `sqlite-vec` directly in `pkg/db/db.go`, we don't need `VectorStore` in `pkg/vector/vector.go` to iterate arrays in memory anymore.
The rule says "替换 pkg/vector/vector.go 中内存 for 循环暴力扫描的逻辑。 解耦性：只需修改 vector.go 和 db.go 的初始化即可，完全不影响解析层和接口层。"

Wait, the rule says:
只需修改 vector.go 和 db.go 的初始化即可，完全不影响解析层和接口层。

This implies `pkg/vector/vector.go` should still have a `VectorStore` struct and the same methods `AddVector`, `RemoveVector`, `Search`, but it will use `db.GraphDB` or take a `*sql.DB` to perform the queries using `sqlite-vec`.
Currently, `NewVectorStore()` returns `*VectorStore`.
If we change `NewVectorStore()` to take an SQLite DB handle, or just `*db.GraphDB`. But that creates a circular dependency or coupling.

Let's see what `main.go` does.
