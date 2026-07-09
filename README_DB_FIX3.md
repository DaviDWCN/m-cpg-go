Wait, if I use `ORDER BY dist ASC LIMIT ?2` I am using full table scan (Critique 2). How to fix this properly in `sqlite-vec`?
If `k = ?` doesn't work, let's see how knn table functions work in sqlite-vec.
`SELECT * FROM vec_items WHERE embedding MATCH ?1 AND k = ?2`
Why did `no such column: k` happen?
Because my table is `vectors(node_id TEXT KEY, embedding float[768])`.
When I do `SELECT ... FROM vectors WHERE embedding MATCH ?1 AND k = ?2`, wait, `k` must be selected from the virtual table itself.
Ah! In my previous patch `WHERE v.node_id IN (SELECT node_id FROM vectors WHERE embedding MATCH ?1 AND k = ?2)`
Did that fail with `no such column: k`? Let's check the logs. Yes.
Is `k` supposed to be `k = ?2` or `k = 5` or what?
Wait! In `sqlite-vec` v0.1.0+, the syntax is:
`SELECT node_id FROM vec_items WHERE embedding MATCH ?1 AND k = ?2`
Why did it fail?
Maybe the virtual table `vec0` does not have a `k` column when defined with `TEXT KEY`?
Let's test locally.
