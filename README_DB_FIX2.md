Wait, if `MATCH` and `k = ?2` is used for `vec0`, the parameter binding is positional (`?1`, `?2`). Let's ensure the `db.Query` uses them correctly in `vector.go`.
