B3 task: 重构 m_cpg_search 的结果合并逻辑。当前仅是简单的列表拼凑，需引入标准的 RRF (Reciprocal Rank Fusion) 将向量检索得分与图谱邻居得分进行科学融合。

Let's rethink `RunSearch`:
Currently:
1. `results := vStore.Search(emb, topK)`
2. For each result `node`:
    - display node
    - display `neighbors`

We need to implement RRF merging vector score and graph neighbor score.
How does that work?
1. Fetch top vectors (e.g. `vector_results := vStore.Search(emb, topK * 2)`)
2. For each result `res` at rank `r_vec` in `vector_results`:
    - Add to node scores: `score(res.ID) += 1.0 / (60 + r_vec)`
    - Get neighbors of `res.ID` (`neighbors := gdb.GetNeighbors(res.ID)`)
    - For each neighbor `n`:
        - Rank of neighbor `r_graph` could be just based on proximity or the rank of the original vector result. Or maybe we just give them a fixed rank based on original node rank. E.g. `score(n.ID) += 1.0 / (60 + r_vec)`.

Wait! What if we query for "topK*2" vectors? Let's say `limit_vec = topK * 3`.
For each `res` in `vector_results` at index `i` (0 to N-1):
  - `rank_vec = i + 1`
  - `nodeScores[res.ID] += 1.0 / (60.0 + float64(rank_vec))`
  - `neighbors, _ := gdb.GetNeighbors(res.ID)`
  - for `n` in neighbors (it's unranked in the graph, so let's say its graph rank is `rank_vec` because it's 1 hop away from rank `rank_vec` match, or just a constant hop penalty like `rank_graph = rank_vec + len(neighbors)`? No, usually in RRF, the graph algorithm produces a ranked list of nodes.
  If the graph algorithm just finds neighbors, we can treat the list of all neighbors of all vector matches as a second retrieval method (e.g., "Graph Neighbors of Top Vector Matches").
  So retrieval 1: Vector Search.
  Retrieval 2: Graph Retrieval (e.g. neighbors).
  Actually, if we just collect all neighbors of top vector matches, count their frequencies, we can rank them by frequency or by the vector rank of their source.
  Let's do this:
  List 1 (Vector matches): `res[0], res[1], ...`
  List 2 (Graph matches): For each node in List 1, get neighbors. Rank neighbors by the sum of vector similarity of their adjacent nodes in List 1. Then sort List 2 to get `graph_rank`.
  Then apply RRF:
  For each node `ID`:
    score = 0
    if in List 1 at `r_vec`: score += 1.0 / (60 + r_vec)
    if in List 2 at `r_graph`: score += 1.0 / (60 + r_graph)

Let's refine:
1. `vector_results := vStore.Search(emb, topK * 3)`
2. `vector_ranks := make(map[string]int)`
   For i, res := range vector_results:
     `vector_ranks[res.ID] = i + 1`
3. `graph_scores := make(map[string]float32)`
   For res in vector_results:
     `neighbors, _ := gdb.GetNeighbors(res.ID)`
     For `n` in neighbors:
        `graph_scores[n["id"].(string)] += res.Score` // sum of vector similarities of adjacent matches
4. Sort `graph_scores` descending to get `graph_ranks` (map[string]int)
5. `rrf_scores := make(map[string]float64)`
   For all unique node IDs in both maps:
     If `rank, ok := vector_ranks[id]`; ok: `rrf_scores[id] += 1.0 / (60.0 + float64(rank))`
     If `rank, ok := graph_ranks[id]`; ok: `rrf_scores[id] += 1.0 / (60.0 + float64(rank))`
6. Sort nodes by `rrf_scores` descending.
7. Take top K nodes.
8. Fetch `node`, its `docstring`, `fqn`, and `code`, and display them as "Hybrid Search Results".
