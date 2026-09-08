package router

import (
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	swaggerFiles "github.com/swaggo/files/v2"
	"github.com/swaggo/swag"
)

// swaggerConfig is the subset of the baseline swagger handler's configuration
// that survives into the rendered page. Field order and the json tags decide
// the byte layout of the `config = {...}` line, so both are load bearing.
type swaggerConfig struct {
	Title                    string           `json:"-"`
	URL                      string           `json:"url,omitempty"`
	Layout                   string           `json:"layout,omitempty"`
	Plugins                  []template.JS    `json:"-"`
	Presets                  []template.JS    `json:"-"`
	DeepLinking              bool             `json:"deepLinking"`
	DefaultModelsExpandDepth int              `json:"defaultModelsExpandDepth,omitempty"`
	DefaultModelExpandDepth  int              `json:"defaultModelExpandDepth,omitempty"`
	DefaultModelRendering    string           `json:"defaultModelRendering,omitempty"`
	DocExpansion             string           `json:"docExpansion,omitempty"`
	ShowMutatedRequest       bool             `json:"showMutatedRequest"`
	CustomStyle              template.CSS     `json:"-"`
	CustomScript             template.JS      `json:"-"`
	Filter                   filterConfig     `json:"-"`
	SyntaxHighlight          *highlightConfig `json:"-"`
	TagsSorter               template.JS      `json:"-"`
	OnComplete               template.JS      `json:"-"`
	RequestInterceptor       template.JS      `json:"-"`
	ResponseInterceptor      template.JS      `json:"-"`
	ModelPropertyMacro       template.JS      `json:"-"`
	ParameterMacro           template.JS      `json:"-"`
	OAuth                    *oauthConfig     `json:"-"`
	PreauthorizeBasic        template.JS      `json:"-"`
	PreauthorizeApiKey       template.JS      `json:"-"`
	InstanceName             string           `json:"-"`
}

type filterConfig struct {
	Enabled    bool
	Expression string
}

func (fc filterConfig) Value() interface{} {
	if fc.Expression != "" {
		return fc.Expression
	}
	return fc.Enabled
}

type highlightConfig struct {
	Activate bool   `json:"activate"`
	Theme    string `json:"theme,omitempty"`
}

func (hc highlightConfig) Value() interface{} {
	if hc.Activate {
		return hc
	}
	return false
}

type oauthConfig struct {
	ClientId string `json:"clientId,omitempty"`
}

func swaggerDefaults() swaggerConfig {
	return swaggerConfig{
		Title:  "Swagger UI",
		Layout: "StandaloneLayout",
		Plugins: []template.JS{
			template.JS("SwaggerUIBundle.plugins.DownloadUrl"),
		},
		Presets: []template.JS{
			template.JS("SwaggerUIBundle.presets.apis"),
			template.JS("SwaggerUIStandalonePreset"),
		},
		DeepLinking:              true,
		DefaultModelsExpandDepth: 1,
		DefaultModelExpandDepth:  1,
		DefaultModelRendering:    "example",
		DocExpansion:             "list",
		SyntaxHighlight:          &highlightConfig{Activate: true, Theme: "agate"},
		ShowMutatedRequest:       true,
	}
}

const swaggerIndexTmpl = `
<!-- HTML for static distribution bundle build -->
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8">
    <title>{{.Title}}</title>
    <link href="https://fonts.googleapis.com/css?family=Open+Sans:400,700|Source+Code+Pro:300,600|Titillium+Web:400,600,700" rel="stylesheet">
    <link rel="stylesheet" type="text/css" href="./swagger-ui.css" >
    <link rel="icon" type="image/png" href="./favicon-32x32.png" sizes="32x32" />
    <link rel="icon" type="image/png" href="./favicon-16x16.png" sizes="16x16" />
    {{- if .CustomStyle}}
      <style>
        body { margin: 0; }
        {{.CustomStyle}}
      </style>
    {{- end}}
    {{- if .CustomScript}}
      <script>
        {{.CustomScript}}
      </script>
    {{- end}}
  </head>
  <body>
    <svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" style="position:absolute;width:0;height:0">
      <defs>
        <symbol viewBox="0 0 20 20" id="unlocked">
              <path d="M15.8 8H14V5.6C14 2.703 12.665 1 10 1 7.334 1 6 2.703 6 5.6V6h2v-.801C8 3.754 8.797 3 10 3c1.203 0 2 .754 2 2.199V8H4c-.553 0-1 .646-1 1.199V17c0 .549.428 1.139.951 1.307l1.197.387C5.672 18.861 6.55 19 7.1 19h5.8c.549 0 1.428-.139 1.951-.307l1.196-.387c.524-.167.953-.757.953-1.306V9.199C17 8.646 16.352 8 15.8 8z"></path>
        </symbol>
        <symbol viewBox="0 0 20 20" id="locked">
          <path d="M15.8 8H14V5.6C14 2.703 12.665 1 10 1 7.334 1 6 2.703 6 5.6V8H4c-.553 0-1 .646-1 1.199V17c0 .549.428 1.139.951 1.307l1.197.387C5.672 18.861 6.55 19 7.1 19h5.8c.549 0 1.428-.139 1.951-.307l1.196-.387c.524-.167.953-.757.953-1.306V9.199C17 8.646 16.352 8 15.8 8zM12 8H8V5.199C8 3.754 8.797 3 10 3c1.203 0 2 .754 2 2.199V8z"/>
        </symbol>
        <symbol viewBox="0 0 20 20" id="close">
          <path d="M14.348 14.849c-.469.469-1.229.469-1.697 0L10 11.819l-2.651 3.029c-.469.469-1.229.469-1.697 0-.469-.469-.469-1.229 0-1.697l2.758-3.15-2.759-3.152c-.469-.469-.469-1.228 0-1.697.469-.469 1.228-.469 1.697 0L10 8.183l2.651-3.031c.469-.469 1.228-.469 1.697 0 .469.469.469 1.229 0 1.697l-2.758 3.152 2.758 3.15c.469.469.469 1.229 0 1.698z"/>
        </symbol>
        <symbol viewBox="0 0 20 20" id="large-arrow">
          <path d="M13.25 10L6.109 2.58c-.268-.27-.268-.707 0-.979.268-.27.701-.27.969 0l7.83 7.908c.268.271.268.709 0 .979l-7.83 7.908c-.268.271-.701.27-.969 0-.268-.269-.268-.707 0-.979L13.25 10z"/>
        </symbol>
        <symbol viewBox="0 0 20 20" id="large-arrow-down">
          <path d="M17.418 6.109c.272-.268.709-.268.979 0s.271.701 0 .969l-7.908 7.83c-.27.268-.707.268-.979 0l-7.908-7.83c-.27-.268-.27-.701 0-.969.271-.268.709-.268.979 0L10 13.25l7.418-7.141z"/>
        </symbol>
        <symbol viewBox="0 0 24 24" id="jump-to">
          <path d="M19 7v4H5.83l3.58-3.59L8 6l-6 6 6 6 1.41-1.41L5.83 13H21V7z"/>
        </symbol>
        <symbol viewBox="0 0 24 24" id="expand">
          <path d="M10 18h4v-2h-4v2zM3 6v2h18V6H3zm3 7h12v-2H6v2z"/>
        </symbol>
      </defs>
    </svg>
    <div id="swagger-ui"></div>
    <script src="./swagger-ui-bundle.js"> </script>
    <script src="./swagger-ui-standalone-preset.js"> </script>
    <script>
    window.onload = function() {
      config = {{.}};
      config.dom_id = '#swagger-ui';
      config.plugins = [
        {{- range $plugin := .Plugins }}
          {{$plugin}},
        {{- end}}
      ];
      config.presets = [
        {{- range $preset := .Presets }}
          {{$preset}},
        {{- end}}
      ];
      config.filter = {{.Filter.Value}}
      config.syntaxHighlight = {{.SyntaxHighlight.Value}}
      {{if .TagsSorter}}
        config.tagsSorter = {{.TagsSorter}}
      {{end}}
      {{if .OnComplete}}
        config.onComplete = {{.OnComplete}}
      {{end}}
      {{if .RequestInterceptor}}
        config.requestInterceptor = {{.RequestInterceptor}}
      {{end}}
      {{if .ResponseInterceptor}}
        config.responseInterceptor = {{.ResponseInterceptor}}
      {{end}}
      {{if .ModelPropertyMacro}}
        config.modelPropertyMacro = {{.ModelPropertyMacro}}
      {{end}}
      {{if .ParameterMacro}}
        config.parameterMacro = {{.ParameterMacro}}
      {{end}}

      const ui = SwaggerUIBundle(config);

      {{if .OAuth}}
        ui.initOAuth({{.OAuth}});
      {{end}}
      {{if .PreauthorizeBasic}}
        ui.preauthorizeBasic({{.PreauthorizeBasic}});
      {{end}}
      {{if .PreauthorizeApiKey}}
        ui.preauthorizeApiKey({{.PreauthorizeApiKey}});
      {{end}}

      window.ui = ui
    }
    </script>
  </body>
</html>
`

// Swagger serves the API documentation the way the baseline did: `index.html`
// is rendered from the template above, `doc.json` is the registered spec, a
// bare prefix redirects to the index, and anything else is looked up in the
// embedded swagger-ui distribution. Files are served by hand rather than
// through http.FileServer so that no Accept-Ranges, nosniff or charset
// decoration reaches the wire, and so the directory listing and the redirect
// rules stay the baseline's.
func Swagger(prefix string) echo.HandlerFunc {
	index := template.Must(template.New("swagger_index.html").Parse(swaggerIndexTmpl))
	cfg := swaggerDefaults()
	cfg.URL = path.Join(prefix, "doc.json")

	return func(c echo.Context) error {
		rest := c.Param("*")
		// The baseline's swagger handler rewrites the request path to the part
		// after the mount prefix before it serves anything, so the access log
		// reports `index.html` rather than `/swagger/index.html`, and an empty
		// string for the bare prefix. The log line is compared byte for byte.
		c.Set(ctxOriginalPath, rest)
		switch rest {
		case "index.html":
			var page strings.Builder
			if err := index.Execute(&page, cfg); err != nil {
				return err
			}
			return sendSwagger(c, "text/html", []byte(page.String()))
		case "doc.json":
			doc, err := swag.ReadDoc(cfg.InstanceName)
			if err != nil {
				return err
			}
			return sendSwagger(c, "application/json", []byte(doc))
		case "", "/":
			return c.Redirect(http.StatusMovedPermanently, path.Join(prefix, "index.html"))
		default:
			return sendSwaggerFile(c, rest)
		}
	}
}

// swaggerTypes maps the extensions the embedded distribution ships to the
// content types the baseline labelled them with. Go's own table answers
// `application/javascript` for `.js`, which is not what the baseline sent.
var swaggerTypes = map[string]string{
	".css":  "text/css",
	".js":   "text/javascript",
	".html": "text/html",
	".png":  "image/png",
	".json": "application/json",
	".map":  "application/json",
}

func sendSwaggerFile(c echo.Context, name string) error {
	clean := strings.TrimPrefix(name, "/")
	if clean == "" || strings.Contains(clean, "..") {
		c.Set(ctxFinalError, true)
		return echo.NewHTTPError(http.StatusInternalServerError,
			"failed to open: open "+name+": invalid argument")
	}
	body, err := fs.ReadFile(swaggerFiles.FS, clean)
	if err != nil {
		c.Set(ctxFinalError, true)
		return echo.NewHTTPError(http.StatusNotFound, "Cannot "+
			OriginalMethod(c)+" "+name)
	}
	ctype := swaggerTypes[strings.ToLower(path.Ext(clean))]
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return sendSwagger(c, ctype, body)
}

func sendSwagger(c echo.Context, ctype string, body []byte) error {
	header := c.Response().Header()
	header.Set(echo.HeaderContentType, ctype)
	header.Set(echo.HeaderContentLength, strconv.Itoa(len(body)))
	c.Response().WriteHeader(http.StatusOK)
	_, err := c.Response().Write(body)
	return err
}
