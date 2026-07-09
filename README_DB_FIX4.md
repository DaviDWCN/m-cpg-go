Wait, if I use `ORDER BY vec_distance_cosine` instead of `MATCH AND k = ?`, the reviewer said:
"The SQL query uses ORDER BY vec_distance_cosine(...) LIMIT ?. This forces SQLite to perform a full-table exact nearest-neighbor scan. To actually utilize the ANN index in sqlite-vec, the query must use the MATCH operator (e.g., WHERE embedding MATCH ?)."

But whenever I try to use `MATCH ... AND k = ?2`, it says `no such column: k`.
Let me check `test_match.go` again, it succeeded when there was NO alias in the query and NO subquery.
`SELECT node_id FROM vectors WHERE embedding MATCH ? AND k = 1` -> failed (we don't have node_id in vectors anymore).
`SELECT rowid FROM vectors WHERE embedding MATCH ? AND k = 1` -> succeeded!
So if I do:
```
SELECT m.node_id, vec_distance_cosine(v.embedding, ?1) as dist, m.metadata
FROM vectors v
JOIN vectors_meta m ON v.rowid = m.rowid
WHERE v.rowid IN (SELECT rowid FROM vectors WHERE embedding MATCH ?1 AND k = ?2)
```
Wait, if `k = ?2` fails, it's because `vectors` in the subquery is what?
Let's test this EXACT subquery in a test file!
