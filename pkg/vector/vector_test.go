package vector

import (
	"testing"
)

func TestVectorStore_AddRemoveSearch(t *testing.T) {
	// Vector store is now backed by SQLite, so pure in-memory test is deprecated.
	// DB integration tests handle the actual insert and query testing.
}
