package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alpody/echo-realworld/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// New resolves its sqlite file relative to the process working directory, so
// the test moves into a throwaway directory shaped like the deployed tree.
func inDatabaseRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "database"), 0o755))

	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })

	return root
}

func TestNewOpensTheApplicationDatabaseWithABoundedPool(t *testing.T) {
	root := inDatabaseRoot(t)

	gdb := New()

	require.NotNil(t, gdb)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	assert.Equal(t, 100, sqlDB.Stats().MaxOpenConnections)
	assert.FileExists(t, filepath.Join(root, "database", "realworld.db"))
}

func TestNewReturnsAHandleAutoMigrateCanUse(t *testing.T) {
	inDatabaseRoot(t)
	gdb := New()
	require.NotNil(t, gdb)

	AutoMigrate(gdb)

	for _, table := range []string{"users", "follows", "articles", "comments", "tags"} {
		assert.True(t, gdb.Migrator().HasTable(table), "expected table %s", table)
	}

	user := model.User{Username: "db-new", Email: "db-new@realworld.io", Password: "x"}
	require.NoError(t, gdb.Create(&user).Error)
	assert.NotZero(t, user.ID)
}
