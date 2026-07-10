package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tempFileDSN returns a file-backed DSN (not :memory:, which is per-connection
// and would hide the pooled-connection bug this test targets).
func tempFileDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "pragma_test.db")
}

func TestNewRepositories_ForeignKeysOnEveryPooledConn(t *testing.T) {
	cfg := Config{DSN: tempFileDSN(t), MaxOpenConns: 2}
	repos, err := NewRepositories(context.Background(), cfg)
	require.NoError(t, err)
	defer func() { _ = repos.Close() }()

	// pin two connections concurrently so both are live in the pool at once,
	// then assert foreign_keys is ON for each.
	conn1, err := repos.DB.Connx(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn1.Close() }()
	conn2, err := repos.DB.Connx(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn2.Close() }()

	var fk1, fk2 int
	require.NoError(t, conn1.GetContext(context.Background(), &fk1, "PRAGMA foreign_keys"))
	require.NoError(t, conn2.GetContext(context.Background(), &fk2, "PRAGMA foreign_keys"))
	assert.Equal(t, 1, fk1, "foreign_keys must be ON for pooled connection 1")
	assert.Equal(t, 1, fk2, "foreign_keys must be ON for pooled connection 2")
}

func TestNewRepositories_CascadeDeleteFiresUnderPool(t *testing.T) {
	cfg := Config{DSN: tempFileDSN(t), MaxOpenConns: 2}
	repos, err := NewRepositories(context.Background(), cfg)
	require.NoError(t, err)
	defer func() { _ = repos.Close() }()

	ctx := context.Background()
	res, err := repos.DB.ExecContext(ctx,
		`INSERT INTO feeds (url, title) VALUES ('http://x/feed', 'x')`)
	require.NoError(t, err)
	feedID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = repos.DB.ExecContext(ctx,
		`INSERT INTO items (feed_id, guid, title, link, published) VALUES (?, 'g1', 't', 'http://x/1', CURRENT_TIMESTAMP)`,
		feedID)
	require.NoError(t, err)

	_, err = repos.DB.ExecContext(ctx, `DELETE FROM feeds WHERE id = ?`, feedID)
	require.NoError(t, err)

	var itemCount int
	require.NoError(t, repos.DB.GetContext(ctx, &itemCount,
		`SELECT COUNT(*) FROM items WHERE feed_id = ?`, feedID))
	assert.Equal(t, 0, itemCount, "cascade delete must remove items when feed is deleted")
}
