package handler

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alpody/echo-realworld/utils"
	"github.com/stretchr/testify/assert"
)

func send(req *http.Request) (*http.Response, string) {
	h.Register(e)
	resp, _ := testRequest(req)
	body, _ := ioutil.ReadAll(resp.Body)
	return resp, string(body)
}

func TestUnknownRoute(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "Cannot GET /nope", body)
	assert.Equal(t, "16", resp.Header.Get("Content-Length"))
}

func TestUnknownRouteUnderApi(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodPost, "/api/nope", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "Cannot POST /api/nope", body)
}

func TestDoubleSlashIsNotCollapsed(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api//tags", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "Cannot GET /api//tags", body)
}

func TestWrongMethod(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodPost, "/api/tags", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "Method Not Allowed", body)
	assert.Equal(t, "GET, HEAD", resp.Header.Get("Allow"))
}

func TestWrongMethodBehindGuard(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodPatch, "/api/user", nil)
	req.Header.Set("Authorization", authHeader(utils.GenerateJWT(1)))
	resp, body := send(req)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, "Method Not Allowed", body)
	assert.Equal(t, "GET, HEAD, PUT", resp.Header.Get("Allow"))
}

func TestGuardRunsBeforeRouting(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodDelete, "/api/users", nil))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "missing or malformed JWT", body)
	assert.Equal(t, "", resp.Header.Get("Allow"))
}

func TestGuardCoversUnroutedPrefix(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api/profileszzz", nil))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "missing or malformed JWT", body)
}

func TestArticlesGuardSkipsReadRequests(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api/articleszzz", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "Cannot GET /api/articleszzz", body)
}

func TestMissingToken(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api/user", nil))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "missing or malformed JWT", body)
}

func TestWrongAuthScheme(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	req.Header.Set("Authorization", "Bearer "+utils.GenerateJWT(1))
	resp, body := send(req)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "missing or malformed JWT", body)
}

func TestInvalidToken(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	req.Header.Set("Authorization", authHeader("not.a.token"))
	resp, body := send(req)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Invalid or expired JWT", body)
}

func TestJSONFraming(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api/tags", nil))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, `{"tags":["tag1","tag2"]}`, body)
	assert.False(t, strings.HasSuffix(body, "\n"))
	assert.Equal(t, "24", resp.Header.Get("Content-Length"))
}

func TestTrailingSlashIsIgnored(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api/tags/", nil))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `{"tags":["tag1","tag2"]}`, body)
}

func TestRootKeepsItsSlash(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "Cannot GET /", body)
}

func TestRoutesMatchWithoutRegardToCase(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/API/TAGS", nil))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `{"tags":["tag1","tag2"]}`, body)
}

func TestParametersKeepTheirCase(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/API/ARTICLES/article1-slug", nil))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `"slug":"article1-slug"`)

	tearDown()
	setup()
	resp, body = send(httptest.NewRequest(http.MethodGet, "/api/articles/Article1-Slug", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, `{"errors":{"body":"resource not found"}}`, body)
}

func TestParametersAreNotUnescaped(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodGet, "/api/articles/article1%2Dslug", nil))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, `{"errors":{"body":"resource not found"}}`, body)
}

func TestHeadIsServedForReadRoutes(t *testing.T) {
	tearDown()
	setup()
	resp, _ := send(httptest.NewRequest(http.MethodHead, "/api/tags", nil))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, "24", resp.Header.Get("Content-Length"))
}

func TestHeadIsNotTreatedAsARead(t *testing.T) {
	tearDown()
	setup()
	resp, _ := send(httptest.NewRequest(http.MethodHead, "/api/articles", nil))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "24", resp.Header.Get("Content-Length"))
}

func TestPreflight(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodOptions, "/api/articles", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, body := send(req)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "", body)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin,Content-Type,Accept,Authorization", resp.Header.Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "GET,HEAD,PUT,PATCH,POST,DELETE", resp.Header.Get("Access-Control-Allow-Methods"))
	assert.Equal(t,
		"Access-Control-Request-Method, Access-Control-Request-Headers, Origin",
		resp.Header.Get("Vary"))
}

func TestPreflightOnUnknownRoute(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodOptions, "/api/nope", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, _ := send(req)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestOptionsWithoutOrigin(t *testing.T) {
	tearDown()
	setup()
	resp, body := send(httptest.NewRequest(http.MethodOptions, "/api/tags", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, "Method Not Allowed", body)
	assert.Equal(t, "GET, HEAD", resp.Header.Get("Allow"))
	assert.Equal(t, "", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestSimpleRequestGetsOnlyTheOriginHeader(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	req.Header.Set("Origin", "http://example.com")
	resp, _ := send(req)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "", resp.Header.Get("Vary"))
	assert.Equal(t, "", resp.Header.Get("Access-Control-Allow-Methods"))
}

func TestUnsupportedContentType(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodPost, "/api/users/login",
		strings.NewReader(`{"user":{"email":"user1@realworld.io","password":"secret"}}`))
	req.Header.Set("Content-type", "text/plain")
	resp, body := send(req)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Equal(t, `{"errors":{"body":"Unprocessable Entity"}}`, body)
}

func TestEmptyBody(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodPost, "/api/users/login", strings.NewReader(""))
	req.Header.Set("Content-type", "application/json")
	resp, body := send(req)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Equal(t, `{"errors":{"body":"unexpected end of JSON input"}}`, body)
}

func TestFormBodyReachesValidation(t *testing.T) {
	tearDown()
	setup()
	req := httptest.NewRequest(http.MethodPost, "/api/users/login",
		strings.NewReader("email=user1@realworld.io&password=secret"))
	req.Header.Set("Content-type", "application/x-www-form-urlencoded")
	resp, body := send(req)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Contains(t, body, "failed on the 'required' tag")
}
