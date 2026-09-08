// End-to-end tests for the behaviour the router package owns: what happens to
// a request before and after a handler gets to see it.
//
// These cases cover paths that match no route and how the request line is
// quoted and HTML-escaped in the answer, the Allow header derived from the
// route table, trailing-slash stripping, case-insensitive routing, empty path
// segments, an unknown HTTP method, the OPTIONS / CORS / preflight contract,
// the credential guards answering unrouted and wrong-method paths ahead of the
// router, HEAD on a list endpoint, JSON framing and Content-Length, and the
// swagger surface mounted by router.Swagger including its refusal to serve a
// path that climbs out of the embedded filesystem.
//
// The application under test is assembled exactly as main.go assembles it. The
// handlers reached here are incidental - what is being asserted is the routing
// and middleware behaviour around them. Handler semantics live in
// handler/e2e_full_stack_test.go.
package router_test

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

// stack is one fully assembled application. The routing cases below need the
// stores only to put a row behind the routes they exercise, so no handle on
// the database itself is kept.
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
// Paths that match no route, and the Allow header
// -------------------------------------------------------------------------

func TestE2EAnUnroutedPathQuotesTheRequestLine(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/nowhere", "", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "Cannot GET /nowhere", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, "19", rec.Header().Get(echo.HeaderContentLength))
}

func TestE2EAnUnroutedPathIsHTMLEscapedInTheMessage(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/api/<b>tag</b>", "", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "Cannot GET /api/&lt;b&gt;tag&lt;/b&gt;",
		rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
	assert.NotContains(t, rec.Body.String(), "<b>")
}

func TestE2EAWrongMethodAnswersWithAnAllowHeader(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodPost, "/api/tags", "", "")

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "Method Not Allowed", rec.Body.String())
	assert.Equal(t, "GET, HEAD", rec.Header().Get(echo.HeaderAllow))
}

// -------------------------------------------------------------------------
// Path normalisation: slashes, case and empty segments
// -------------------------------------------------------------------------

func TestE2ETrailingSlashesAreStripped(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("slasher", "slasher@realworld.io", "secret12")
	s.seedArticle(author, "slash-piece", "Slash Piece", "go")

	rec := s.json(http.MethodGet, "/api/tags///", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []interface{}{"go"}, body(t, rec)["tags"])
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2ERoutingIsCaseInsensitive(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("caser", "caser@realworld.io", "secret12")
	s.seedArticle(author, "case-piece", "Case Piece", "go")

	rec := s.json(http.MethodGet, "/API/TAGS", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []interface{}{"go"}, body(t, rec)["tags"])
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
}

func TestE2EAnEmptyPathSegmentMatchesNoRoute(t *testing.T) {
	s := newStack(t)

	// Echo's router would happily bind an empty `:slug`; restoreParams must
	// refuse it so no row is ever addressed by the empty string.
	rec := s.json(http.MethodGet, "/api/articles//comments", "", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "Cannot GET /api/articles//comments", rec.Body.String())
	assert.Empty(t, rec.Header().Get(echo.HeaderAllow))
}

// -------------------------------------------------------------------------
// Unknown methods, OPTIONS and the CORS contract
// -------------------------------------------------------------------------

func TestE2EAnUnknownHTTPMethodIsABadRequest(t *testing.T) {
	s := newStack(t)

	rec := s.json("BREW", "/api/tags", "", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "Invalid http method", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8",
		rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, "19", rec.Header().Get(echo.HeaderContentLength))
}

func TestE2EAnOptionsWithoutAnOriginIsNotAPreflight(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodOptions, "/api/tags", "", "")

	// No route registers OPTIONS, and without an Origin the CORS middleware
	// leaves the request alone, so the router reports method not allowed.
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get(echo.HeaderAllow))
	assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
}

func TestE2EAPreflightIsAnsweredWithTheCORSContract(t *testing.T) {
	s := newStack(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/articles", nil)
	req.Header.Set(echo.HeaderOrigin, "http://client.example")
	req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodPost)
	rec := s.do(req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "*", rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
	assert.Equal(t, "GET,HEAD,PUT,PATCH,POST,DELETE",
		rec.Header().Get(echo.HeaderAccessControlAllowMethods))
	assert.Equal(t, "Origin,Content-Type,Accept,Authorization",
		rec.Header().Get(echo.HeaderAccessControlAllowHeaders))
	assert.Equal(t,
		"Access-Control-Request-Method, Access-Control-Request-Headers, Origin",
		rec.Header().Get(echo.HeaderVary))
	assert.Empty(t, rec.Body.String())
}

func TestE2EAnOptionsWithAnOriginButNoRequestMethodIsRoutedNormally(
	t *testing.T,
) {
	s := newStack(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/tags", nil)
	req.Header.Set(echo.HeaderOrigin, "http://client.example")
	rec := s.do(req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, echo.HeaderOrigin, rec.Header().Get(echo.HeaderVary))
}

func TestE2EACrossOriginReadIsTagged(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("corser", "corser@realworld.io", "secret12")
	s.seedArticle(author, "cors-piece", "Cors Piece", "go")

	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	req.Header.Set(echo.HeaderOrigin, "http://client.example")
	rec := s.do(req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "*", rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
}

func TestE2EASameOriginReadIsNotTagged(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("plain", "plain@realworld.io", "secret12")
	s.seedArticle(author, "plain-piece", "Plain Piece", "go")

	rec := s.json(http.MethodGet, "/api/tags", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
	assert.Empty(t, rec.Header().Get(echo.HeaderVary))
}

// -------------------------------------------------------------------------
// The credential guards run ahead of the router
// -------------------------------------------------------------------------

func TestE2EAGuardAnswersAPathThatMatchesNoRoute(t *testing.T) {
	s := newStack(t)

	// /api/user/anything routes nowhere, but the credential guard is mounted
	// on the /api/user prefix and runs before the 404 is rendered.
	rec := s.json(http.MethodGet, "/api/user/anything", "", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing or malformed JWT", rec.Body.String())
}

func TestE2EAGuardedUnroutedPathWithAValidTokenFallsBackToNotFound(
	t *testing.T,
) {
	s := newStack(t)
	u := s.seedUser("guarded", "guarded@realworld.io", "secret12")

	rec := s.json(http.MethodGet, "/api/user/anything", "",
		utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "Cannot GET /api/user/anything", rec.Body.String())
}

func TestE2EAWrongMethodBehindAGuardIsRejectedByTheGuardFirst(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodPatch, "/api/user", "", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing or malformed JWT", rec.Body.String())
}

func TestE2EAWrongMethodBehindAGuardWithACredentialIs405(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("patcher", "patcher@realworld.io", "secret12")

	rec := s.json(http.MethodPatch, "/api/user", "", utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD, PUT", rec.Header().Get(echo.HeaderAllow))
}

func TestE2EPostingToTheFeedPathIsMethodNotAllowed(t *testing.T) {
	s := newStack(t)
	u := s.seedUser("poster", "poster@realworld.io", "secret12")

	rec := s.json(http.MethodPost, "/api/articles/feed", "",
		utils.GenerateJWT(u.ID))

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	// allowHeader derives the list from the route table, so `/articles/:slug`
	// contributes PUT and DELETE alongside the literal `/articles/feed` pair.
	assert.Equal(t, "GET, HEAD, PUT, DELETE",
		rec.Header().Get(echo.HeaderAllow))
}

// -------------------------------------------------------------------------
// Response framing on the routes that do match
// -------------------------------------------------------------------------

func TestE2EHeadOnAListEndpointKeepsTheJSONHeaders(t *testing.T) {
	s := newStack(t)
	author := s.seedUser("header", "header@realworld.io", "secret12")
	s.seedArticle(author, "head-piece", "Head Piece", "go")

	rec := s.json(http.MethodHead, "/api/tags", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, echo.MIMEApplicationJSON,
		rec.Header().Get(echo.HeaderContentType))
	assert.NotEmpty(t, rec.Header().Get(echo.HeaderContentLength))
}

func TestE2EJSONResponsesStateTheirLengthAndCarryNoTrailingNewline(
	t *testing.T,
) {
	s := newStack(t)
	author := s.seedUser("lengthy", "lengthy@realworld.io", "secret12")
	s.seedArticle(author, "length-piece", "Length Piece", "go")

	rec := s.json(http.MethodGet, "/api/tags", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, fmt.Sprintf("%d", rec.Body.Len()),
		rec.Header().Get(echo.HeaderContentLength))
	assert.False(t, strings.HasSuffix(rec.Body.String(), "\n"),
		"the serializer must not append a newline")
}

// -------------------------------------------------------------------------
// The swagger surface mounted by router.Swagger
// -------------------------------------------------------------------------

func TestE2ESwaggerIndexRenders(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/swagger/index.html", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html", rec.Header().Get(echo.HeaderContentType))
	assert.Contains(t, rec.Body.String(), `config.dom_id = '#swagger-ui'`)
	assert.Contains(t, rec.Body.String(), `"url":"/swagger/doc.json"`)
	assert.NotContains(t, rec.Body.String(), "doc.yaml")
	assert.Equal(t, fmt.Sprintf("%d", rec.Body.Len()),
		rec.Header().Get(echo.HeaderContentLength))
}

func TestE2ESwaggerServesTheRegisteredSpec(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/swagger/doc.json", "", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json",
		rec.Header().Get(echo.HeaderContentType))
	spec := body(t, rec)
	assert.Equal(t, "2.0", spec["swagger"])
	assert.Equal(t, "/api", spec["basePath"])
	paths, ok := spec["paths"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, paths, "/articles")
	assert.Contains(t, paths, "/users/login")
}

func TestE2ESwaggerBarePrefixRedirectsToTheIndex(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/swagger", "", "")

	assert.Equal(t, http.StatusMovedPermanently, rec.Code)
	assert.Equal(t, "/swagger/index.html",
		rec.Header().Get(echo.HeaderLocation))
}

func TestE2ESwaggerServesItsStaticAssets(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/swagger/swagger-ui.css", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/css", rec.Header().Get(echo.HeaderContentType))
	assert.Empty(t, rec.Header().Get("Accept-Ranges"))
	assert.Empty(t, rec.Header().Get("X-Content-Type-Options"))
	assert.NotEmpty(t, rec.Body.Bytes())
}

func TestE2ESwaggerReportsAMissingAsset(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/swagger/no-such-asset.js", "", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "Cannot GET no-such-asset.js", rec.Body.String())
}

func TestE2ESwaggerRefusesToLeaveItsOwnDirectory(t *testing.T) {
	s := newStack(t)

	rec := s.json(http.MethodGet, "/swagger/../main.go", "", "")

	// The wildcard captures `../main.go` verbatim; sendSwaggerFile refuses any
	// name containing `..` rather than reading outside the embedded FS.
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "failed to open: open ../main.go: invalid argument",
		rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "package main")
}
