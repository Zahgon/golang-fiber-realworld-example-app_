// End-to-end tests for the behaviour the utils package owns: the JWT the
// application hands out, and the shape of the error envelope it renders.
//
// utils.GenerateJWT is asserted through the real credential guard rather than
// by taking the token apart, so the claim the guard reads back is the claim
// that was written; and utils.NewValidatorError is asserted through a real
// rejected request, so the envelope under test is the one a client would
// actually receive. Both therefore need the application assembled the way
// main.go assembles it.
package utils_test

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

// stack is one fully assembled application. gdb backs the assertion that a
// rejected request wrote nothing; us seeds the identities whose tokens are
// put through the guard.
type stack struct {
	t   *testing.T
	app *echo.Echo
	gdb *gorm.DB
	us  *store.UserStore
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

	return &stack{t: t, app: app, gdb: gdb, us: us}
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

// -------------------------------------------------------------------------
// Tokens carry an identity; validation failures carry an envelope
// -------------------------------------------------------------------------

func TestE2EGeneratedTokensCarryTheUserIdentity(t *testing.T) {
	s := newStack(t)
	first := s.seedUser("idone", "idone@realworld.io", "secret12")
	second := s.seedUser("idtwo", "idtwo@realworld.io", "secret12")

	// Each token must resolve to exactly its own user through the real guard.
	one := s.json(http.MethodGet, "/api/user", "",
		utils.GenerateJWT(first.ID))
	require.Equal(t, http.StatusOK, one.Code, one.Body.String())
	assert.Equal(t, "idone", object(t, one, "user")["username"])

	two := s.json(http.MethodGet, "/api/user", "",
		utils.GenerateJWT(second.ID))
	require.Equal(t, http.StatusOK, two.Code, two.Body.String())
	assert.Equal(t, "idtwo", object(t, two, "user")["username"])

	assert.Equal(t, "idone@realworld.io", object(t, one, "user")["email"])
	assert.Equal(t, "idtwo@realworld.io", object(t, two, "user")["email"])
	assert.NotEqual(t, utils.GenerateJWT(first.ID), utils.GenerateJWT(second.ID),
		"two identities must not share a token")
}

func TestE2EValidationErrorsAreRenderedAsAnErrorEnvelope(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodPost, "/api/users",
		`{"user":{"username":"","email":"","password":""}}`, "")

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	errs := object(t, rec, "errors")
	require.NotEmpty(t, errs)
	assert.Contains(t, fmt.Sprintf("%v", errs), "required")
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
	var users int64
	require.NoError(t, s.gdb.Model(&model.User{}).Count(&users).Error)
	assert.Equal(t, int64(0), users)
}
