package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/alpody/echo-realworld/db"
	"github.com/alpody/echo-realworld/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type listFixture struct {
	store  *ArticleStore
	author model.User
	other  model.User
	oldest model.Article
	newest model.Article
}

func newListFixture(t *testing.T) listFixture {
	t.Helper()

	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "store.db")), &gorm.Config{})
	require.NoError(t, err)
	db.AutoMigrate(gdb)

	author := model.User{Username: "list-author", Email: "list-author@realworld.io", Password: "x"}
	other := model.User{Username: "list-other", Email: "list-other@realworld.io", Password: "x"}
	require.NoError(t, gdb.Create(&author).Error)
	require.NoError(t, gdb.Create(&other).Error)

	zeta := model.Tag{Tag: "zeta"}
	alpha := model.Tag{Tag: "alpha"}
	require.NoError(t, gdb.Create(&zeta).Error)
	require.NoError(t, gdb.Create(&alpha).Error)

	oldest := model.Article{
		Model:    gorm.Model{CreatedAt: time.Now().Add(-2 * time.Hour)},
		Slug:     "list-oldest",
		Title:    "oldest title",
		Body:     "oldest body",
		AuthorID: author.ID,
		Tags:     []model.Tag{zeta, alpha},
	}
	newest := model.Article{
		Model:     gorm.Model{CreatedAt: time.Now().Add(-1 * time.Hour)},
		Slug:      "list-newest",
		Title:     "newest title",
		Body:      "newest body",
		AuthorID:  other.ID,
		Tags:      []model.Tag{zeta},
		Favorites: []model.User{author},
	}
	require.NoError(t, gdb.Create(&oldest).Error)
	require.NoError(t, gdb.Create(&newest).Error)

	return listFixture{store: NewArticleStore(gdb), author: author, other: other, oldest: oldest, newest: newest}
}

func slugsOf(articles []model.Article) []string {
	slugs := make([]string, 0, len(articles))
	for _, a := range articles {
		slugs = append(slugs, a.Slug)
	}
	return slugs
}

func TestArticleStoreListByTagReturnsNewestFirst(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByTag("zeta", 0, 10)

	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	assert.Equal(t, []string{"list-newest", "list-oldest"}, slugsOf(articles))
}

func TestArticleStoreListByTagPaginatesWithoutChangingCount(t *testing.T) {
	f := newListFixture(t)

	first, count, err := f.store.ListByTag("zeta", 0, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	assert.Equal(t, []string{"list-newest"}, slugsOf(first))

	second, count, err := f.store.ListByTag("zeta", 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	assert.Equal(t, []string{"list-oldest"}, slugsOf(second))
}

func TestArticleStoreListByTagPreloadsAuthorAndSortedTags(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByTag("alpha", 0, 10)

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	require.Len(t, articles, 1)
	assert.Equal(t, "list-oldest", articles[0].Slug)
	assert.Equal(t, "list-author", articles[0].Author.Username)
	require.Len(t, articles[0].Tags, 2)
	assert.Equal(t, "alpha", articles[0].Tags[0].Tag)
	assert.Equal(t, "zeta", articles[0].Tags[1].Tag)
}

func TestArticleStoreListByTagUnknownTagIsAnError(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByTag("no-such-tag", 0, 10)

	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	assert.Nil(t, articles)
	assert.Equal(t, int64(0), count)
}

func TestArticleStoreListByAuthorReturnsOnlyThatAuthor(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByAuthor("list-author", 0, 10)

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	require.Len(t, articles, 1)
	assert.Equal(t, "list-oldest", articles[0].Slug)
	assert.Equal(t, f.author.ID, articles[0].AuthorID)
	assert.Equal(t, "list-author", articles[0].Author.Username)
}

func TestArticleStoreListByAuthorUnknownUserIsAnError(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByAuthor("ghost", 0, 10)

	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	assert.Nil(t, articles)
	assert.Equal(t, int64(0), count)
}

func TestArticleStoreListByWhoFavoritedReturnsTheFavoritedArticles(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByWhoFavorited("list-author", 0, 10)

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	require.Len(t, articles, 1)
	assert.Equal(t, "list-newest", articles[0].Slug)
	assert.Equal(t, "list-other", articles[0].Author.Username)
}

func TestArticleStoreListByWhoFavoritedSkipsArticlesNobodyFavorited(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByWhoFavorited("list-other", 0, 10)

	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
	assert.Empty(t, articles)
}

func TestArticleStoreListByWhoFavoritedUnknownUserIsAnError(t *testing.T) {
	f := newListFixture(t)

	articles, count, err := f.store.ListByWhoFavorited("ghost", 0, 10)

	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	assert.Nil(t, articles)
	assert.Equal(t, int64(0), count)
}
