package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newContext(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return echo.New().NewContext(req, rec), rec
}

func TestDeserializeDecodesTheRequestBody(t *testing.T) {
	c, _ := newContext(t, `{"user":{"email":"jake@jake.jake","bio":"i work"}}`)

	var payload struct {
		User struct {
			Email string `json:"email"`
			Bio   string `json:"bio"`
		} `json:"user"`
	}
	err := jsonSerializer{}.Deserialize(c, &payload)

	require.NoError(t, err)
	assert.Equal(t, "jake@jake.jake", payload.User.Email)
	assert.Equal(t, "i work", payload.User.Bio)
}

func TestDeserializeIgnoresFieldsTheTargetDoesNotDeclare(t *testing.T) {
	c, _ := newContext(t, `{"title":"kept","unknown":{"deep":1}}`)

	var payload struct {
		Title string `json:"title"`
	}
	err := jsonSerializer{}.Deserialize(c, &payload)

	require.NoError(t, err)
	assert.Equal(t, "kept", payload.Title)
}

func TestDeserializeReportsMalformedJSON(t *testing.T) {
	c, _ := newContext(t, `{"title":`)

	var payload struct {
		Title string `json:"title"`
	}
	err := jsonSerializer{}.Deserialize(c, &payload)

	assert.Error(t, err)
	assert.Empty(t, payload.Title)
}

func swaggerRequest(t *testing.T, target string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	c.Set(ctxOriginalMethod, http.MethodGet)
	c.Set(ctxOriginalPath, target)
	c.SetParamNames("*")
	c.SetParamValues(strings.TrimPrefix(target, "/swagger/"))
	return c, rec
}

func TestSwaggerIndexIsHTMLWithNoCharsetAndAnExplicitLength(t *testing.T) {
	c, rec := swaggerRequest(t, "/swagger/index.html")

	require.NoError(t, Swagger("/swagger")(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html", rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get(echo.HeaderContentLength))
	assert.Contains(t, rec.Body.String(), `config.dom_id = '#swagger-ui'`)
}

func TestSwaggerIndexPointsAtTheSingleRegisteredSpec(t *testing.T) {
	c, rec := swaggerRequest(t, "/swagger/index.html")

	require.NoError(t, Swagger("/swagger")(c))

	assert.Contains(t, rec.Body.String(), `"url":"/swagger/doc.json"`)
	assert.NotContains(t, rec.Body.String(), "doc.yaml")
}

func TestSwaggerServesStaticAssetsWithoutRangeOrSniffHeaders(t *testing.T) {
	c, rec := swaggerRequest(t, "/swagger/swagger-ui.css")

	require.NoError(t, Swagger("/swagger")(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/css", rec.Header().Get(echo.HeaderContentType))
	assert.Empty(t, rec.Header().Get("Accept-Ranges"))
	assert.Empty(t, rec.Header().Get("X-Content-Type-Options"))
}

func TestSwaggerLabelsScriptsTextJavascript(t *testing.T) {
	c, rec := swaggerRequest(t, "/swagger/swagger-ui-bundle.js")

	require.NoError(t, Swagger("/swagger")(c))

	assert.Equal(t, "text/javascript", rec.Header().Get(echo.HeaderContentType))
}

func TestSwaggerRedirectsTheBarePrefixToTheIndex(t *testing.T) {
	c, rec := swaggerRequest(t, "/swagger/")
	c.SetParamValues("")

	require.NoError(t, Swagger("/swagger")(c))

	assert.Equal(t, http.StatusMovedPermanently, rec.Code)
	assert.Equal(t, "/swagger/index.html", rec.Header().Get(echo.HeaderLocation))
}

func TestSwaggerReportsAMissingAssetAsAFinalNotFound(t *testing.T) {
	c, _ := swaggerRequest(t, "/swagger/nosuchfile")

	err := Swagger("/swagger")(c)

	var he *echo.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, http.StatusNotFound, he.Code)
	assert.Equal(t, "Cannot GET nosuchfile", he.Message)
	assert.NotNil(t, c.Get(ctxFinalError))
}

func TestSwaggerRefusesToEscapeItsOwnDirectory(t *testing.T) {
	c, _ := swaggerRequest(t, "/swagger/../api/tags")

	err := Swagger("/swagger")(c)

	var he *echo.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, http.StatusInternalServerError, he.Code)
	assert.Equal(t, "failed to open: open ../api/tags: invalid argument", he.Message)
}
