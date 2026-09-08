// End-to-end tests for the behaviour the handler package owns: the sign up /
// sign in / current-user round trip, the JWT credential contract the guard
// enforces, the request decoders behind Bind (JSON, urlencoded forms,
// multipart forms), profiles and follows, the article lifecycle, the feed,
// tags, comments and favourites.
//
// Each case drives the application assembled exactly as main.go assembles it -
// a real router.New(), the real middleware chain, the real credential guards,
// the real stores and a real sqlite database built by db.AutoMigrate. Nothing
// is mocked and nothing is stubbed: every case issues an HTTP request and
// asserts on the status line, the headers, the response body and - where the
// request mutates state - on what the store and the database hold afterwards.
//
// Routing, CORS and the swagger surface belong to the router package and are
// exercised by router/e2e_full_stack_test.go; schema and persistence belong to
// db/e2e_full_stack_test.go.
package handler_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alpody/echo-realworld/db"
	_ "github.com/alpody/echo-realworld/docs"
	"github.com/alpody/echo-realworld/handler"
	"github.com/alpody/echo-realworld/model"
	"github.com/alpody/echo-realworld/router"
	"github.com/alpody/echo-realworld/store"
	"github.com/alpody/echo-realworld/utils"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stack is one fully assembled application: router, middleware, handlers,
// stores and database, wired exactly as main.go wires them.
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
// Registration, login and the current user
// -------------------------------------------------------------------------

func TestE2ERegisterLoginAndReadBackTheCurrentUser(t *testing.T) {
	s := newStack(t)

	token := s.register("jake", "jake@realworld.io", "jakejake")
	require.NotEmpty(t, token)

	login := s.json(http.MethodPost, "/api/users/login",
		`{"user":{"email":"jake@realworld.io","password":"jakejake"}}`, "")
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	assert.Equal(t, "jake", object(t, login, "user")["username"])

	me := s.json(http.MethodGet, "/api/user", "",
		object(t, login, "user")["token"].(string))
	require.Equal(t, http.StatusOK, me.Code, me.Body.String())
	assert.Equal(t, "jake@realworld.io", object(t, me, "user")["email"])

	// The row really exists behind the API.
	stored, err := s.us.GetByEmail("jake@realworld.io")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "jake", stored.Username)
	assert.NotEqual(t, "jakejake", stored.Password, "password must be hashed")
	assert.True(t, stored.CheckPassword("jakejake"))
}

func TestE2ERegisterRejectsADuplicateEmail(t *testing.T) {
	s := newStack(t)
	s.register("first", "dup@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/users",
		`{"user":{"username":"second","email":"dup@realworld.io","password":"secret12"}}`,
		"")

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "errors")
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
	var users int64
	require.NoError(t, s.gdb.Model(&model.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), users, "the duplicate must not be written")
}

func TestE2ERegisterRejectsAMissingUsername(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodPost, "/api/users",
		`{"user":{"email":"nouser@realworld.io","password":"secret12"}}`, "")

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, object(t, rec, "errors")["body"], "Username")
	stored, err := s.us.GetByEmail("nouser@realworld.io")
	require.NoError(t, err)
	assert.Nil(t, stored, "a rejected sign up must write no row")
}

func TestE2ELoginWithTheWrongPasswordIsForbidden(t *testing.T) {
	s := newStack(t)
	s.register("carol", "carol@realworld.io", "correct1")

	rec := s.json(http.MethodPost, "/api/users/login",
		`{"user":{"email":"carol@realworld.io","password":"wrongwrong"}}`, "")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "access forbidden", object(t, rec, "errors")["body"])
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
	assert.NotContains(t, rec.Body.String(), "token")
}

func TestE2ELoginWithAnUnknownEmailIsForbidden(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodPost, "/api/users/login",
		`{"user":{"email":"ghost@realworld.io","password":"secret12"}}`, "")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "access forbidden", object(t, rec, "errors")["body"])
	assert.NotContains(t, rec.Body.String(), "token")
}

func TestE2EUpdateUserWritesEveryFieldThrough(t *testing.T) {
	s := newStack(t)
	token := s.register("dave", "dave@realworld.io", "secret12")

	rec := s.json(http.MethodPut, "/api/user", `{"user":{"username":"dave",`+
		`"email":"dave@new.io","bio":"writes go","image":"http://img/d.png"}}`,
		token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	user := object(t, rec, "user")
	assert.Equal(t, "dave@new.io", user["email"])
	assert.Equal(t, "writes go", user["bio"])
	assert.Equal(t, "http://img/d.png", user["image"])

	stored, err := s.us.GetByEmail("dave@new.io")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.Bio)
	assert.Equal(t, "writes go", *stored.Bio)
}

func TestE2EUpdateUserRehashesANewPassword(t *testing.T) {
	s := newStack(t)
	token := s.register("erin", "erin@realworld.io", "oldsecret")

	rec := s.json(http.MethodPut, "/api/user",
		`{"user":{"username":"erin","email":"erin@realworld.io",`+
			`"password":"newsecret"}}`, token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	login := s.json(http.MethodPost, "/api/users/login",
		`{"user":{"email":"erin@realworld.io","password":"newsecret"}}`, "")
	assert.Equal(t, http.StatusOK, login.Code, login.Body.String())

	old := s.json(http.MethodPost, "/api/users/login",
		`{"user":{"email":"erin@realworld.io","password":"oldsecret"}}`, "")
	assert.Equal(t, http.StatusForbidden, old.Code)
}

func TestE2ECurrentUserForADeletedIdentityIsNotFound(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/api/user", "", utils.GenerateJWT(4242))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
}

// -------------------------------------------------------------------------
// The credential guard
// -------------------------------------------------------------------------

func TestE2EMissingAuthorizationIsReportedAsMalformed(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/api/user", "", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing or malformed JWT", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2EATokenTooShortToBeACredentialIsMalformed(t *testing.T) {
	s := newStack(t)

	req := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	// The scheme plus exactly one character: the length test is strictly
	// greater than len("Token ")+1, so this is malformed, not unauthorised.
	req.Header.Set(echo.HeaderAuthorization, "Token x")
	rec := s.do(req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing or malformed JWT", rec.Body.String())
}

func TestE2EAnUnparsableTokenIsUnauthorised(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/api/user", "", "not.a.jwt.at.all")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "Invalid or expired JWT", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, "22", rec.Header().Get(echo.HeaderContentLength))
}

func TestE2EAnExpiredTokenIsUnauthorised(t *testing.T) {
	s := newStack(t)
	s.seedUser("expired", "expired@realworld.io", "secret12")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":  1,
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	signed, err := token.SignedString(utils.JWTSecret)
	require.NoError(t, err)

	rec := s.json(http.MethodGet, "/api/user", "", signed)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "Invalid or expired JWT", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2EStrongerHMACAlgorithmsAreAccepted(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("hmac", "hmac@realworld.io", "secret12")

	for _, method := range []jwt.SigningMethod{
		jwt.SigningMethodHS384, jwt.SigningMethodHS512,
	} {
		token := jwt.NewWithClaims(method, jwt.MapClaims{
			"id":  u.ID,
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		signed, err := token.SignedString(utils.JWTSecret)
		require.NoError(t, err)

		rec := s.json(http.MethodGet, "/api/user", "", signed)

		require.Equalf(t, http.StatusOK, rec.Code,
			"%s should authenticate: %s", method.Alg(), rec.Body.String())
		assert.Equal(t, "hmac", object(t, rec, "user")["username"])
	}
}

func TestE2EATokenSignedWithTheWrongKeyIsRejected(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("forged", "forged@realworld.io", "secret12")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":  u.ID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("not the server secret"))
	require.NoError(t, err)

	rec := s.json(http.MethodGet, "/api/user", "", signed)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "Invalid or expired JWT", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "forged")
}

// -------------------------------------------------------------------------
// Request body decoding: JSON, urlencoded forms and multipart forms
// -------------------------------------------------------------------------

func TestE2ERegisterThroughAURLEncodedForm(t *testing.T) {
	s := newStack(t)

	rec := s.request(http.MethodPost, "/api/users",
		echo.MIMEApplicationForm,
		"user[username]=frank&user[email]=frank%40realworld.io"+
			"&user[password]=secret12", "")

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	user := object(t, rec, "user")
	assert.Equal(t, "frank", user["username"])
	// `%40` must have been decoded to `@` by the form decoder.
	assert.Equal(t, "frank@realworld.io", user["email"])

	stored, err := s.us.GetByUsername("frank")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "frank@realworld.io", stored.Email)
}

func TestE2EFormDecodingTurnsPlusIntoSpaceAndKeepsBadEscapes(t *testing.T) {
	s := newStack(t)
	token := s.register("grace", "grace@realworld.io", "secret12")

	// `+` decodes to a space; `%zz` is not a valid escape and the baseline's
	// decoder passes the `%` through literally instead of failing the request.
	rec := s.request(http.MethodPut, "/api/user", echo.MIMEApplicationForm,
		"user[username]=grace&user[email]=grace%40realworld.io"+
			"&user[bio]=hello+world+%zz", token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "hello world %zz", object(t, rec, "user")["bio"])
}

func TestE2EFormDecodingAcceptsUppercaseHexEscapes(t *testing.T) {
	s := newStack(t)
	token := s.register("heidi", "heidi@realworld.io", "secret12")

	// %2F is upper case hex, %2f is lower case; both must decode to `/`.
	rec := s.request(http.MethodPut, "/api/user", echo.MIMEApplicationForm,
		"user[username]=heidi&user[email]=heidi%40realworld.io"+
			"&user[bio]=a%2Fb%2fc", token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "a/b/c", object(t, rec, "user")["bio"])
}

func TestE2EFormDecodingKeepsATruncatedEscapeLiteral(t *testing.T) {
	s := newStack(t)
	token := s.register("ivan", "ivan@realworld.io", "secret12")

	// A `%` in the last two bytes cannot start a complete escape; the
	// baseline's decoder emits the remaining bytes untouched.
	rec := s.request(http.MethodPut, "/api/user", echo.MIMEApplicationForm,
		"user[username]=ivan&user[email]=ivan%40realworld.io&user[bio]=done%2",
		token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "done%2", object(t, rec, "user")["bio"])
}

func TestE2ERegisterThroughAMultipartForm(t *testing.T) {
	s := newStack(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("user[username]", "judy"))
	require.NoError(t, w.WriteField("user[email]", "judy@realworld.io"))
	require.NoError(t, w.WriteField("user[password]", "secret12"))
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/users", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	rec := s.do(req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "judy", object(t, rec, "user")["username"])

	stored, err := s.us.GetByUsername("judy")
	require.NoError(t, err)
	require.NotNil(t, stored)
}

func TestE2EAMultipartBodyWithoutABoundaryIsUnprocessable(t *testing.T) {
	s := newStack(t)

	rec := s.request(http.MethodPost, "/api/users",
		echo.MIMEMultipartForm, "whatever", "")

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, object(t, rec, "errors")["body"], "boundary")
}

func TestE2EAnUnsupportedContentTypeIsUnprocessable(t *testing.T) {
	s := newStack(t)

	rec := s.request(http.MethodPost, "/api/users",
		"text/plain", "username=kate", "")

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "Unprocessable Entity", object(t, rec, "errors")["body"])
}

func TestE2EAVendorJSONMediaTypeIsDecodedAsJSON(t *testing.T) {
	s := newStack(t)

	rec := s.request(http.MethodPost, "/api/users",
		"application/vnd.api+json",
		`{"user":{"username":"leo","email":"leo@realworld.io",`+
			`"password":"secret12"}}`, "")

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "leo", object(t, rec, "user")["username"])
}

// -------------------------------------------------------------------------
// Profiles and following
// -------------------------------------------------------------------------

func TestE2EProfileReportsTheFollowRelationBothWays(t *testing.T) {
	s := newStack(t)
	follower := s.seedUser("follower", "follower@realworld.io", "secret12")
	s.seedUser("celeb", "celeb@realworld.io", "secret12")
	token := utils.GenerateJWT(follower.ID)

	before := s.json(http.MethodGet, "/api/profiles/celeb", "", token)
	require.Equal(t, http.StatusOK, before.Code, before.Body.String())
	assert.Equal(t, false, object(t, before, "profile")["following"])

	follow := s.json(http.MethodPost, "/api/profiles/celeb/follow", "", token)
	require.Equal(t, http.StatusOK, follow.Code, follow.Body.String())
	assert.Equal(t, true, object(t, follow, "profile")["following"])

	after := s.json(http.MethodGet, "/api/profiles/celeb", "", token)
	require.Equal(t, http.StatusOK, after.Code)
	assert.Equal(t, true, object(t, after, "profile")["following"])

	unfollow := s.json(http.MethodDelete, "/api/profiles/celeb/follow", "",
		token)
	require.Equal(t, http.StatusOK, unfollow.Code, unfollow.Body.String())
	assert.Equal(t, false, object(t, unfollow, "profile")["following"])

	// And the relation really is gone from the database.
	celeb, err := s.us.GetByUsername("celeb")
	require.NoError(t, err)
	isFollower, err := s.us.IsFollower(celeb.ID, follower.ID)
	require.NoError(t, err)
	assert.False(t, isFollower)
}

func TestE2EProfileOfAnUnknownUserIsNotFound(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("someone", "someone@realworld.io", "secret12")

	rec := s.json(http.MethodGet, "/api/profiles/nobody", "",
		utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2EFollowingAnUnknownUserIsNotFound(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("chaser", "chaser@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/profiles/nobody/follow", "",
		utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
	var follows int64
	require.NoError(t, s.gdb.Model(&model.Follow{}).Count(&follows).Error)
	assert.Equal(t, int64(0), follows, "no follow row may be written")
}

func TestE2EProfilePathKeepsItsOriginalCase(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("MixedCase", "mixed@realworld.io", "secret12")

	// normalize() lowercases the path for routing; restoreParams() must put
	// the original-case segment back or the lookup would miss the row.
	rec := s.json(http.MethodGet, "/api/profiles/MixedCase", "",
		utils.GenerateJWT(u.ID))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "MixedCase", object(t, rec, "profile")["username"])
}

// -------------------------------------------------------------------------
// Articles, the feed and tags
// -------------------------------------------------------------------------

func TestE2ECreateArticleDerivesTheSlugAndPersistsTags(t *testing.T) {
	s := newStack(t)
	token := s.register("writer", "writer@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/articles",
		`{"article":{"title":"How To Train Your Dragon",`+
			`"description":"Ever wonder how?","body":"You have to believe",`+
			`"tagList":["dragons","training"]}}`, token)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	article := object(t, rec, "article")
	assert.Equal(t, "how-to-train-your-dragon", article["slug"])
	assert.Equal(t, "How To Train Your Dragon", article["title"])

	stored, err := s.as.GetBySlug("how-to-train-your-dragon")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "You have to believe", stored.Body)
	require.Len(t, stored.Tags, 2)
	assert.Equal(t, "dragons", stored.Tags[0].Tag)
	assert.Equal(t, "training", stored.Tags[1].Tag)
}

func TestE2ECreateArticleWithoutATitleIsUnprocessable(t *testing.T) {
	s := newStack(t)
	token := s.register("halfwriter", "half@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/articles",
		`{"article":{"description":"d","body":"b"}}`, token)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	_, count, err := s.as.List(0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "a rejected article must not be written")
}

func TestE2ECreateArticleRequiresACredential(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodPost, "/api/articles",
		`{"article":{"title":"t","description":"d","body":"b"}}`, "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing or malformed JWT", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
	_, count, err := s.as.List(0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestE2EReadingAnArticleNeedsNoCredential(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("reader-author", "ra@realworld.io", "secret12")
	s.seedArticle(author, "public-piece", "Public Piece", "open")

	rec := s.json(http.MethodGet, "/api/articles/public-piece", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	article := object(t, rec, "article")
	assert.Equal(t, "Public Piece", article["title"])
	assert.Equal(t, []interface{}{"open"}, article["tagList"])
	assert.Equal(t, float64(0), article["favoritesCount"])
	assert.Equal(t, false, article["favorited"])
}

func TestE2EAnUnknownSlugIsNotFound(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/api/articles/no-such-slug", "", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2EArticleListIsNewestFirstAndCounted(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("lister", "lister@realworld.io", "secret12")
	older := s.seedArticle(author, "older-one", "Older One", "alpha")
	newer := s.seedArticle(author, "newer-one", "Newer One", "beta")

	// Make the ordering unambiguous rather than relying on clock resolution.
	require.NoError(t, s.gdb.Model(older).
		Update("created_at", time.Now().Add(-2*time.Hour)).Error)
	require.NoError(t, s.gdb.Model(newer).
		Update("created_at", time.Now().Add(-1*time.Hour)).Error)

	rec := s.json(http.MethodGet, "/api/articles", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := body(t, rec)
	assert.Equal(t, float64(2), payload["articlesCount"])
	articles := payload["articles"].([]interface{})
	require.Len(t, articles, 2)
	assert.Equal(t, "newer-one",
		articles[0].(map[string]interface{})["slug"])
	assert.Equal(t, "older-one",
		articles[1].(map[string]interface{})["slug"])
}

func TestE2EArticleListCarriesTheAuthorProfile(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("profiled", "profiled@realworld.io", "secret12")
	s.seedArticle(author, "with-author", "With Author")

	rec := s.json(http.MethodGet, "/api/articles", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	articles := body(t, rec)["articles"].([]interface{})
	require.Len(t, articles, 1)
	first := articles[0].(map[string]interface{})
	got := first["author"].(map[string]interface{})
	assert.Equal(t, "profiled", got["username"])
	assert.Equal(t, "profiled bio", got["bio"])
	assert.Equal(t, false, got["following"])
}

func TestE2EArticleListIgnoresQueryStringFilters(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("qfilter", "qfilter@realworld.io", "secret12")
	s.seedArticle(author, "tagged-a", "Tagged A", "alpha")
	s.seedArticle(author, "tagged-b", "Tagged B", "beta")

	// The handler reads its filters with c.Param, not c.QueryParam, which is
	// what the Fiber baseline did. A query string therefore selects nothing
	// and the full list comes back. This is baseline parity, not an accident,
	// and it must not drift.
	rec := s.json(http.MethodGet, "/api/articles?tag=alpha", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, float64(2), body(t, rec)["articlesCount"])
}

func TestE2EUpdateArticleRewritesTheSlugAndReplacesTags(t *testing.T) {
	s := newStack(t)
	token := s.register("owner", "owner@realworld.io", "secret12")
	created := s.json(http.MethodPost, "/api/articles",
		`{"article":{"title":"First Title","description":"d","body":"b",`+
			`"tagList":["one"]}}`, token)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	rec := s.json(http.MethodPut, "/api/articles/first-title",
		`{"article":{"title":"Second Title","description":"d2","body":"b2",`+
			`"tagList":["two","three"]}}`, token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "second-title", object(t, rec, "article")["slug"])

	stored, err := s.as.GetBySlug("second-title")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "b2", stored.Body)
	tags := make([]string, 0, len(stored.Tags))
	for _, tag := range stored.Tags {
		tags = append(tags, tag.Tag)
	}
	assert.ElementsMatch(t, []string{"three", "two"}, tags)
}

func TestE2EUpdatingSomebodyElsesArticleIsNotFound(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("real-owner", "ro@realworld.io", "secret12")
	intruder := s.seedUser("intruder", "in@realworld.io", "secret12")
	s.seedArticle(author, "not-yours", "Not Yours")

	rec := s.json(http.MethodPut, "/api/articles/not-yours",
		`{"article":{"title":"Hijacked","description":"d","body":"b"}}`,
		utils.GenerateJWT(intruder.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	stored, err := s.as.GetBySlug("not-yours")
	require.NoError(t, err)
	require.NotNil(t, stored, "the article must survive the attempt")
	assert.Equal(t, "Not Yours", stored.Title)
}

func TestE2EDeleteArticleRemovesItFromTheStore(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("deleter", "deleter@realworld.io", "secret12")
	s.seedArticle(author, "doomed", "Doomed")

	rec := s.json(http.MethodDelete, "/api/articles/doomed", "",
		utils.GenerateJWT(author.ID))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "ok", body(t, rec)["result"])

	stored, err := s.as.GetBySlug("doomed")
	require.NoError(t, err)
	assert.Nil(t, stored)
}

func TestE2EDeletingAnUnknownArticleIsNotFound(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("nihilist", "nihilist@realworld.io", "secret12")

	rec := s.json(http.MethodDelete, "/api/articles/never-existed", "",
		utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
}

func TestE2EFeedIsEmptyUntilYouFollowSomebody(t *testing.T) {
	s := newStack(t)
	me := s.seedUser("feeder", "feeder@realworld.io", "secret12")
	other := s.seedUser("posted", "posted@realworld.io", "secret12")
	s.seedArticle(other, "their-piece", "Their Piece")

	rec := s.json(http.MethodGet, "/api/articles/feed", "",
		utils.GenerateJWT(me.ID))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, body(t, rec)["articles"])
}

func TestE2EFeedShowsArticlesByTheAuthorsYouFollow(t *testing.T) {
	s := newStack(t)
	me := s.seedUser("subscriber", "sub@realworld.io", "secret12")
	other := s.seedUser("published", "pub@realworld.io", "secret12")
	s.seedArticle(other, "followed-piece", "Followed Piece")
	s.seedArticle(me, "my-own-piece", "My Own Piece")

	token := utils.GenerateJWT(me.ID)
	follow := s.json(http.MethodPost, "/api/profiles/published/follow", "",
		token)
	require.Equal(t, http.StatusOK, follow.Code, follow.Body.String())

	rec := s.json(http.MethodGet, "/api/articles/feed", "", token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	articles := body(t, rec)["articles"].([]interface{})
	require.Len(t, articles, 1)
	assert.Equal(t, "followed-piece",
		articles[0].(map[string]interface{})["slug"])
}

func TestE2EFeedAlwaysRequiresACredential(t *testing.T) {
	s := newStack(t)

	// /api/articles/feed is the one GET the skipper does not exempt.
	rec := s.json(http.MethodGet, "/api/articles/feed", "", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing or malformed JWT", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2ETagsListsEveryTagEverUsed(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("tagger", "tagger@realworld.io", "secret12")
	s.seedArticle(author, "tag-piece", "Tag Piece", "go", "sqlite")

	rec := s.json(http.MethodGet, "/api/tags", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	tags := body(t, rec)["tags"].([]interface{})
	assert.ElementsMatch(t, []interface{}{"go", "sqlite"}, tags)
}

func TestE2ETagsOnAnEmptyDatabaseIsAnInternalError(t *testing.T) {
	s := newStack(t)

	// ListTags reports "tags not found" rather than an empty list, which the
	// handler turns into a 500. Baseline parity.
	rec := s.json(http.MethodGet, "/api/tags", "", "")

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "tags not found", object(t, rec, "errors")["body"])
}

// -------------------------------------------------------------------------
// Comments
// -------------------------------------------------------------------------

func TestE2ECommentIsStoredAndListedWithItsAuthor(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("commenter", "commenter@realworld.io", "secret12")
	s.seedArticle(author, "talked-about", "Talked About")
	token := utils.GenerateJWT(author.ID)

	created := s.json(http.MethodPost,
		"/api/articles/talked-about/comments",
		`{"comment":{"body":"Nice piece"}}`, token)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.Equal(t, "Nice piece", object(t, created, "comment")["body"])

	listed := s.json(http.MethodGet, "/api/articles/talked-about/comments",
		"", "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	comments := body(t, listed)["comments"].([]interface{})
	require.Len(t, comments, 1)
	got := comments[0].(map[string]interface{})
	assert.Equal(t, "Nice piece", got["body"])
	assert.Equal(t, "commenter",
		got["author"].(map[string]interface{})["username"])

	stored, err := s.as.GetCommentsBySlug("talked-about")
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "Nice piece", stored[0].Body)
}

func TestE2ECommentingOnAnUnknownArticleIsNotFound(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("shouter", "shouter@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/articles/nowhere/comments",
		`{"comment":{"body":"hello?"}}`, utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
	var comments int64
	require.NoError(t, s.gdb.Model(&model.Comment{}).Count(&comments).Error)
	assert.Equal(t, int64(0), comments)
}

func TestE2EAnEmptyCommentBodyIsUnprocessable(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("terse", "terse@realworld.io", "secret12")
	s.seedArticle(author, "terse-piece", "Terse Piece")

	rec := s.json(http.MethodPost, "/api/articles/terse-piece/comments",
		`{"comment":{}}`, utils.GenerateJWT(author.ID))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	stored, err := s.as.GetCommentsBySlug("terse-piece")
	require.NoError(t, err)
	assert.Empty(t, stored)
}

func TestE2EYouCanDeleteYourOwnComment(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("selfdel", "selfdel@realworld.io", "secret12")
	s.seedArticle(author, "self-del", "Self Del")
	token := utils.GenerateJWT(author.ID)
	created := s.json(http.MethodPost, "/api/articles/self-del/comments",
		`{"comment":{"body":"mine"}}`, token)
	require.Equal(t, http.StatusCreated, created.Code)
	id := int(object(t, created, "comment")["id"].(float64))

	rec := s.json(http.MethodDelete,
		fmt.Sprintf("/api/articles/self-del/comments/%d", id), "", token)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	stored, err := s.as.GetCommentsBySlug("self-del")
	require.NoError(t, err)
	assert.Empty(t, stored)
}

func TestE2EYouCannotDeleteSomebodyElsesComment(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("owner2", "owner2@realworld.io", "secret12")
	stranger := s.seedUser("stranger", "stranger@realworld.io", "secret12")
	s.seedArticle(author, "shared-piece", "Shared Piece")
	created := s.json(http.MethodPost, "/api/articles/shared-piece/comments",
		`{"comment":{"body":"protected"}}`, utils.GenerateJWT(author.ID))
	require.Equal(t, http.StatusCreated, created.Code)
	id := int(object(t, created, "comment")["id"].(float64))

	rec := s.json(http.MethodDelete,
		fmt.Sprintf("/api/articles/shared-piece/comments/%d", id), "",
		utils.GenerateJWT(stranger.ID))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "unathorized action", object(t, rec, "errors")["body"])
	stored, err := s.as.GetCommentsBySlug("shared-piece")
	require.NoError(t, err)
	assert.Len(t, stored, 1, "the comment must survive")
}

func TestE2EADeleteWithANonNumericCommentIDIsABadRequest(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("numeric", "numeric@realworld.io", "secret12")
	s.seedArticle(author, "numeric-piece", "Numeric Piece")

	rec := s.json(http.MethodDelete,
		"/api/articles/numeric-piece/comments/abc", "",
		utils.GenerateJWT(author.ID))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, object(t, rec, "errors")["body"], "invalid syntax")
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2EDeletingAnUnknownCommentIsNotFound(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("ghosthunter", "gh@realworld.io", "secret12")
	s.seedArticle(author, "ghost-piece", "Ghost Piece")

	rec := s.json(http.MethodDelete,
		"/api/articles/ghost-piece/comments/9999", "",
		utils.GenerateJWT(author.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
}

// -------------------------------------------------------------------------
// Favourites
// -------------------------------------------------------------------------

func TestE2EFavouriteThenUnfavouriteMovesTheCount(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("favauthor", "fa@realworld.io", "secret12")
	fan := s.seedUser("fan", "fan@realworld.io", "secret12")
	s.seedArticle(author, "loved-piece", "Loved Piece")
	token := utils.GenerateJWT(fan.ID)

	fav := s.json(http.MethodPost, "/api/articles/loved-piece/favorite", "",
		token)
	require.Equal(t, http.StatusOK, fav.Code, fav.Body.String())
	article := object(t, fav, "article")
	assert.Equal(t, true, article["favorited"])
	assert.Equal(t, float64(1), article["favoritesCount"])

	unfav := s.json(http.MethodDelete,
		"/api/articles/loved-piece/favorite", "", token)
	require.Equal(t, http.StatusOK, unfav.Code, unfav.Body.String())
	assert.Equal(t, float64(0), object(t, unfav, "article")["favoritesCount"])

	stored, err := s.as.GetBySlug("loved-piece")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Empty(t, stored.Favorites)
}

func TestE2EFavouritingAnUnknownArticleIsNotFound(t *testing.T) {
	s := newStack(t)
	fan := s.seedUser("lonelyfan", "lf@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/articles/nothing/favorite", "",
		utils.GenerateJWT(fan.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "resource not found", object(t, rec, "errors")["body"])
}
