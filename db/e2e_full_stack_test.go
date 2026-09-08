// End-to-end tests for the behaviour the db package owns: the schema
// db.AutoMigrate builds, and the fact that what the API accepts over HTTP is
// what actually lands in - and leaves - that schema.
//
// The three cases here assert against the *gorm.DB handle directly rather than
// against a store, because the subject is the database itself: that every
// table and every column the API needs exists, that a write accepted over HTTP
// produces rows, and that a delete accepted over HTTP is a gorm.Model soft
// delete rather than a physical one. Reaching those assertions needs a live
// application on top of the database, so the stack is assembled exactly as
// main.go assembles it and the rows are produced by real HTTP requests.
package db_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpody/echo-realworld/db"
	_ "github.com/alpody/echo-realworld/docs"
	"github.com/alpody/echo-realworld/handler"
	"github.com/alpody/echo-realworld/model"
	"github.com/alpody/echo-realworld/router"
	"github.com/alpody/echo-realworld/store"
	"github.com/alpody/echo-realworld/utils"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stack is one fully assembled application over a migrated sqlite database.
// gdb is the handle the cases below assert against.
type stack struct {
	t   *testing.T
	app *echo.Echo
	gdb *gorm.DB
	us  *store.UserStore
	as  *store.ArticleStore
}

// newStack assembles the application the way main.go does, over a private
// sqlite file so the case is independent of every other case.
func newStack(t *testing.T) *stack {
	t.Helper()

	gdb, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "e2e.db")), &gorm.Config{})
	require.NoError(t, err)
	db.AutoMigrate(gdb)

	app := router.New()
	swagger := router.Swagger("/swagger")
	app.GET("/swagger", swagger)
	app.HEAD("/swagger", swagger)
	app.GET("/swagger/*", swagger)
	app.HEAD("/swagger/*", swagger)

	us := store.NewUserStore(gdb)
	as := store.NewArticleStore(gdb)
	handler.NewHandler(us, as).Register(app)

	return &stack{t: t, app: app, gdb: gdb, us: us, as: as}
}

// do sends one request through the assembled application.
func (s *stack) do(req *http.Request) *httptest.ResponseRecorder {
	s.t.Helper()
	rec := httptest.NewRecorder()
	s.app.ServeHTTP(rec, req)
	return rec
}

// request sends a request with an explicit content type and credential.
func (s *stack) request(
	method, target, ctype, body, token string,
) *httptest.ResponseRecorder {
	s.t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	if ctype != "" {
		req.Header.Set(echo.HeaderContentType, ctype)
	}
	if token != "" {
		req.Header.Set(echo.HeaderAuthorization, "Token "+token)
	}
	return s.do(req)
}

// json sends an application/json request.
func (s *stack) json(
	method, target, body, token string,
) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.request(method, target, echo.MIMEApplicationJSON, body, token)
}

// body decodes a JSON response body into a map.
func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m),
		"body was %q", rec.Body.String())
	return m
}

// object reads a nested JSON object out of a response body.
func object(
	t *testing.T, rec *httptest.ResponseRecorder, key string,
) map[string]interface{} {
	t.Helper()
	m := body(t, rec)
	nested, ok := m[key].(map[string]interface{})
	require.Truef(t, ok, "expected object %q in %q", key, rec.Body.String())
	return nested
}

// register creates a user over HTTP and returns the token the API issued.
func (s *stack) register(username, email, password string) string {
	s.t.Helper()
	rec := s.json(http.MethodPost, "/api/users", fmt.Sprintf(
		`{"user":{"username":%q,"email":%q,"password":%q}}`,
		username, email, password), "")
	require.Equalf(s.t, http.StatusCreated, rec.Code,
		"sign up failed: %s", rec.Body.String())
	return object(s.t, rec, "user")["token"].(string)
}

// seedUser writes a user straight through the store, for cases whose subject
// is not registration. The password is hashed by the model, as the handler
// would hash it.
func (s *stack) seedUser(username, email, password string) *model.User {
	s.t.Helper()
	bio := username + " bio"
	image := "http://realworld.io/" + username + ".jpg"
	u := &model.User{
		Username: username,
		Email:    email,
		Bio:      &bio,
		Image:    &image,
	}
	hashed, err := u.HashPassword(password)
	require.NoError(s.t, err)
	u.Password = hashed
	require.NoError(s.t, s.us.Create(u))
	return u
}

// seedArticle writes an article straight through the store.
func (s *stack) seedArticle(
	author *model.User, slug, title string, tags ...string,
) *model.Article {
	s.t.Helper()
	a := &model.Article{
		Slug:        slug,
		Title:       title,
		Description: title + " description",
		Body:        title + " body",
		AuthorID:    author.ID,
	}
	for _, tag := range tags {
		a.Tags = append(a.Tags, model.Tag{Tag: tag})
	}
	require.NoError(s.t, s.as.CreateArticle(a))
	return a
}

// -------------------------------------------------------------------------
// The schema, and what the API writes into it
// -------------------------------------------------------------------------

func TestE2EAutoMigrateBuildsEverySchemaTheAPINeeds(t *testing.T) {
	s := newStack(t)

	for _, table := range []string{
		"users", "follows", "articles", "comments", "tags",
		"favorites", "article_tags",
	} {
		assert.Truef(t, s.gdb.Migrator().HasTable(table),
			"AutoMigrate must create %q", table)
	}
	assert.True(t, s.gdb.Migrator().HasColumn(&model.User{}, "email"))
	assert.True(t, s.gdb.Migrator().HasColumn(&model.User{}, "password"))
	assert.True(t, s.gdb.Migrator().HasColumn(&model.Article{}, "slug"))
	assert.True(t, s.gdb.Migrator().HasColumn(&model.Article{}, "author_id"))
	assert.True(t, s.gdb.Migrator().HasColumn(&model.Comment{}, "article_id"))
	assert.True(t, s.gdb.Migrator().HasColumn(&model.Tag{}, "tag"))
	assert.True(t, s.gdb.Migrator().HasColumn(&model.Article{}, "deleted_at"),
		"soft deletes need the gorm.Model tombstone column")
}

func TestE2EWritesThroughTheAPILandInTheDatabase(t *testing.T) {
	s := newStack(t)
	token := s.register("persisted", "persisted@realworld.io", "secret12")
	created := s.json(http.MethodPost, "/api/articles",
		`{"article":{"title":"Durable","description":"d","body":"b",`+
			`"tagList":["durable"]}}`, token)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	var users, articles, tags int64
	require.NoError(t, s.gdb.Model(&model.User{}).Count(&users).Error)
	require.NoError(t, s.gdb.Model(&model.Article{}).Count(&articles).Error)
	require.NoError(t, s.gdb.Model(&model.Tag{}).Count(&tags).Error)

	assert.Equal(t, int64(1), users)
	assert.Equal(t, int64(1), articles)
	assert.Equal(t, int64(1), tags)
}

func TestE2EDeletesThroughTheAPIAreSoftDeletes(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("softy", "softy@realworld.io", "secret12")
	s.seedArticle(author, "soft-piece", "Soft Piece")

	rec := s.json(http.MethodDelete, "/api/articles/soft-piece", "",
		utils.GenerateJWT(author.ID))
	require.Equal(t, http.StatusOK, rec.Code)

	var live, all int64
	require.NoError(t, s.gdb.Model(&model.Article{}).Count(&live).Error)
	require.NoError(t, s.gdb.Unscoped().
		Model(&model.Article{}).Count(&all).Error)

	assert.Equal(t, int64(0), live)
	assert.Equal(t, int64(1), all, "gorm.Model deletes are soft deletes")
}
