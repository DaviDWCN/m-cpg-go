Great! Using `github.com/asg017/sqlite-vec-go-bindings/ncruces` and `github.com/ncruces/go-sqlite3` allows us to use pure Go (WASM) without CGO, satisfying Rule 1 while adding ANN vector index support!

Let's review the requirements:
- B1: 引入 ANN 向量索引 (sqlite-vec)。替换 pkg/vector/vector.go 中内存 for 循环暴力扫描。只需修改 vector.go 和 db.go 的初始化即可。
- B2: Embedding 降级与兜底策略 (Fallback Embeddings)。当外部 API 不可用时，当前的随机伪嵌入会导致语义丢失。需引入一个本地词袋 (BoW) / TF-IDF 或纯本地的微型 Embedding。
- B3: 混合检索排序算法优化 (Hybrid Search Reciprocal Rank Fusion)。重构 m_cpg_search 的结果合并逻辑。引入 RRF。

For B1: We will use `github.com/ncruces/go-sqlite3` and `github.com/asg017/sqlite-vec-go-bindings/ncruces`. We will replace `modernc.org/sqlite`. We need to initialize the sqlite-vec extension and create a `vectors` table (or replace the existing one) with `vec0`.
Let's look at `db.go` schema.

For B2: Fallback Embeddings. Currently, `vector.GeneratePseudoEmbedding(text)` in `pkg/vector/embed.go` generates deterministic random vectors. We should implement a lightweight TF-IDF or BoW representation (which is sparse), but the embedding API expects a fixed dense size. Or we can just use a simple frequency-based dense vector, but what size? The current pseudo-embedding generates 768 dimensions. We can do a hashing trick (Feature Hashing) where we hash tokens into buckets and keep it dense (e.g. 768 buckets) and return those as the fallback embedding.
Let's see: BoW hashing to 768 dimensions is exactly Feature Hashing (Hashing Trick). We can parse tokens, hash them to 0-767, and increment the value. Then L2 normalize the vector.

For B3: Hybrid Search Reciprocal Rank Fusion. `RunSearch` in `mcp.go` gets graph memories (vector store) and episodic/semantic memories (event memories via `RunSearchMemory`).
Currently it just concatenates the output. We should perform RRF on both sources, or at least if they represent the same IDs. But wait, VectorStore returns Code Nodes (with FQN), and `RunSearchMemory` returns Events. They are completely different entities. How to RRF?
Ah, "混合检索排序算法优化" in B3 says: "重构 m_cpg_search 的结果合并逻辑。引入标准的 RRF 将向量检索得分与图谱邻居得分进行科学融合。"
Wait, it's Vector Search Score + Graph Neighbor Score! Currently, we get vector results and then fetch neighbors. We should probably score nodes based on Vector Score + Graph proximity.

Let's look closely at B3 description:
"重构 m_cpg_search 的结果合并逻辑。当前仅是简单的列表拼凑，需引入标准的 RRF (Reciprocal Rank Fusion) 将向量检索得分与图谱邻居得分进行科学融合。"
