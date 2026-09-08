// End-to-end tests for the behaviour the store package owns: that the store
// query surface reports exactly what the API wrote through it.
//
// Both cases drive a write over HTTP against the application assembled the way
// main.go assembles it, and then read the result back through the store's own
// listing methods rather than through the API, so a disagreement between the
// two - a filter that selects the wrong rows, a count that does not match the
// page, a join that loses the favouriting user - shows up as a failure here.
package store_test

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

// stack is one fully assembled application. as is the store the cases below
// interrogate after the API has written through it.
type stack struct {
	t   *testing.T
	app *echo.Echo
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

	return &stack{t: t, app: app, us: us, as: as}
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
// Store listings against what the API wrote
// -------------------------------------------------------------------------

func TestE2EStoreListingsAgreeWithWhatTheAPIWrote(t *testing.T) {
	s := newStack(t)
	token := s.register("agreeable", "agreeable@realworld.io", "secret12")
	created := s.json(http.MethodPost, "/api/articles",
		`{"article":{"title":"Agreed","description":"d","body":"b",`+
			`"tagList":["agreed"]}}`, token)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	byTag, tagCount, err := s.as.ListByTag("agreed", 0, 20)
	require.NoError(t, err)
	require.Len(t, byTag, 1)
	assert.Equal(t, "agreed", byTag[0].Slug)
	assert.Equal(t, int64(1), tagCount)

	byAuthor, authorCount, err := s.as.ListByAuthor("agreeable", 0, 20)
	require.NoError(t, err)
	require.Len(t, byAuthor, 1)
	assert.Equal(t, int64(1), authorCount)

	tags, err := s.as.ListTags()
	require.NoError(t, err)
	require.Len(t, tags, 1)
	assert.Equal(t, "agreed", tags[0].Tag)
}

func TestE2EFavouritesAreListableByWhoFavourited(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("favlister", "favlister@realworld.io", "secret12")
	fan := s.seedUser("favfan", "favfan@realworld.io", "secret12")
	s.seedArticle(author, "listed-piece", "Listed Piece")

	rec := s.json(http.MethodPost, "/api/articles/listed-piece/favorite", "",
		utils.GenerateJWT(fan.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	favourited, count, err := s.as.ListByWhoFavorited("favfan", 0, 20)
	require.NoError(t, err)
	require.Len(t, favourited, 1)
	assert.Equal(t, "listed-piece", favourited[0].Slug)
	assert.Equal(t, int64(1), count)
}
