Aha! The `m_cpg.db` was persisting from before the schema change, causing `no such column: k` because it was NOT a virtual table!
The `rm m_cpg.db` fixed the E2E test!
This implies `MATCH ?1 AND k = ?2` works perfectly.
