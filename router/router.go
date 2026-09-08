package router

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	// ctxOriginalPath and ctxOriginalMethod keep the request line as it
	// arrived, because normalize() rewrites both before routing.
	ctxOriginalPath   = "_original_path"
	ctxOriginalMethod = "_original_method"
	// ctxMatchPath is the original-case path after trailing-slash removal.
	ctxMatchPath = "_match_path"
	// ctxFinalError marks an error a handler already phrased completely, so the
	// error handler renders it as it stands instead of rewriting it into the
	// router's own "no such route" or "method not allowed" wording.
	ctxFinalError = "_final_error"
	// ctxLogMessage carries the rendered error text to the access log, which
	// reports what the client was told rather than the raw error.
	ctxLogMessage = "_log_message"
	// ctxGuardAnswered marks a request the credential guard answered itself.
	// In the baseline that guard runs as prefix middleware ahead of routing and
	// returns no error, so nothing reaches the access log's error column even
	// on a path that has no route; here the same guard runs from the error
	// handler, where the router's own 404 is still in flight.
	ctxGuardAnswered = "_guard_answered"
	// methodSkipOptions replaces OPTIONS on a request that carries no Origin
	// header. No route registers it, so the router reports "method not
	// allowed" instead of answering the preflight itself.
	methodSkipOptions = "SKIP-OPTIONS"
)

// methodOrder is the order response Allow headers list methods in. OPTIONS is
// absent because the router advertises it on every path while an Allow header
// only ever names methods a route was registered for.
var methodOrder = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodDelete,
	http.MethodConnect,
	http.MethodTrace,
	http.MethodPatch,
}

// Guard pairs a path prefix with the middleware mounted on it. Prefix-scoped
// middleware runs before the router decides whether a route exists, so on an
// unmatched request the guard's rejection wins over the 404 or 405.
type Guard struct {
	Prefix     string
	Middleware echo.MiddlewareFunc
}

// New builds the application router.
func New() *echo.Echo {
	f := echo.New()
	f.HideBanner = true
	f.HidePort = true
	// net/http answers `OPTIONS *` itself with an empty 200 before the handler
	// is ever called. The baseline has no such interception and routes the
	// asterisk target like any other, which it has no route for, so it reports
	// `Cannot OPTIONS *`.
	f.Server.DisableGeneralOptionsHandler = true
	f.JSONSerializer = jsonSerializer{}
	f.HTTPErrorHandler = ErrorHandler()
	f.Pre(normalize)
	f.Use(accessLog(os.Stdout))
	f.Use(cors(corsConfig{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, HEAD, PUT, PATCH, POST, DELETE",
	}))
	f.Use(restoreParams)
	return f
}

// jsonSerializer marshals responses without the trailing newline that
// encoding/json's Encoder appends, and always states the body length.
type jsonSerializer struct{}

func (jsonSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	var (
		b   []byte
		err error
	)
	if indent != "" {
		b, err = json.MarshalIndent(i, "", indent)
	} else {
		b, err = json.Marshal(i)
	}
	if err != nil {
		return err
	}
	c.Response().Header().Set(echo.HeaderContentLength, strconv.Itoa(len(b)))
	_, err = c.Response().Write(b)
	return err
}

func (jsonSerializer) Deserialize(c echo.Context, i interface{}) error {
	return json.NewDecoder(c.Request().Body).Decode(i)
}

// knownMethods is the set of method tokens the baseline's HTTP server accepts.
// fasthttp answers any other token with `400 Invalid http method` before the
// router is consulted, and the comparison is byte exact, so an unknown token
// never reaches a route.
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodDelete: true, http.MethodConnect: true,
	http.MethodOptions: true, http.MethodTrace: true, http.MethodPatch: true,
}

// rawRequestPath returns the request target byte for byte as the client wrote
// it. url.URL re-encodes every byte net/url considers unsafe, so EscapedPath
// reports `/api/%3Cb%3E` for a request line that said `/api/<b>` and the 404
// body would quote an escape the client never sent. RequestURI carries the
// original bytes and is only unusable for the two request-target forms that are
// not a path - absolute-form and the asterisk-form - where the parsed URL is
// the right answer and is what the baseline reports.
func rawRequestPath(req *http.Request) string {
	target := req.RequestURI
	if query := strings.IndexByte(target, '?'); query != -1 {
		target = target[:query]
	}
	if !strings.HasPrefix(target, "/") {
		return req.URL.EscapedPath()
	}
	return target
}

// normalize records the incoming request line, then lowercases the path and
// drops EVERY trailing slash so routing is case insensitive and lenient, which
// is what the baseline's router does.
func normalize(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()

		raw := rawRequestPath(req)
		c.Set(ctxOriginalPath, raw)
		c.Set(ctxOriginalMethod, req.Method)

		if !knownMethods[req.Method] {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid http method")
		}

		match := raw
		for len(match) > 1 && strings.HasSuffix(match, "/") {
			match = match[:len(match)-1]
		}
		c.Set(ctxMatchPath, match)

		lower := strings.ToLower(match)
		req.URL.RawPath = lower
		unescaped, err := url.PathUnescape(lower)
		if err != nil {
			unescaped = lower
		}
		req.URL.Path = unescaped

		// A preflight is an OPTIONS carrying BOTH an Origin and an
		// Access-Control-Request-Method. Anything else is an ordinary OPTIONS:
		// hide the method from the router so it answers "method not allowed"
		// exactly as the baseline does.
		if req.Method == http.MethodOptions &&
			(req.Header.Get(echo.HeaderOrigin) == "" ||
				req.Header.Get(echo.HeaderAccessControlRequestMethod) == "") {
			req.Method = methodSkipOptions
		}
		return next(c)
	}
}

// restoreParams puts the original-case path segments back into the route
// parameters, which the lowercasing done by normalize() would otherwise lose.
func restoreParams(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		names := c.ParamNames()
		pattern := c.Path()
		if len(names) > 0 && pattern != "" {
			segments := strings.Split(strings.TrimPrefix(stringValue(c, ctxMatchPath), "/"), "/")
			values := make([]string, len(names))
			i := 0
			for n, part := range strings.Split(strings.TrimPrefix(pattern, "/"), "/") {
				if i >= len(names) || n >= len(segments) {
					break
				}
				switch {
				case strings.HasPrefix(part, ":"):
					// An empty segment does not satisfy `:param` in the
					// baseline's router, so a path like /api/articles//comments
					// matches no route there. Echo's router does match it and
					// would hand the handler an empty slug, which writes rows
					// the baseline never writes.
					if segments[n] == "" {
						return echo.ErrNotFound
					}
					values[i] = segments[n]
					i++
				case part == "*":
					values[i] = strings.Join(segments[n:], "/")
					i++
				}
			}
			if i == len(names) {
				c.SetParamValues(values...)
			}
		}
		return next(c)
	}
}

// accessLog reproduces the baseline's request log line for line:
// `${time} | ${status} | ${latency} | ${ip} | ${method} | ${path} | ${error}`
// with the clock in 15:04:05, the latency right aligned in 13 columns, the
// request path exactly as it arrived, and `-` where no error was raised.
func accessLog(out io.Writer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			if err != nil {
				c.Error(err)
			}
			stop := time.Now()

			message := "-"
			if err != nil && c.Get(ctxGuardAnswered) == nil {
				message = err.Error()
				if he, ok := err.(*echo.HTTPError); ok {
					message = messageOf(he)
				}
				if rendered := stringValue(c, ctxLogMessage); rendered != "" {
					message = rendered
				}
			}
			address := c.Request().RemoteAddr
			if host, _, splitErr := net.SplitHostPort(address); splitErr == nil {
				address = host
			}
			fmt.Fprintf(out, "%s | %d | %13v | %s | %s | %s | %s\n",
				stop.Format("15:04:05"), c.Response().Status, stop.Sub(start),
				address, stringValue(c, ctxOriginalMethod),
				stringValue(c, ctxOriginalPath), message)
			return nil
		}
	}
}

type corsConfig struct {
	AllowOrigins string
	AllowHeaders string
	AllowMethods string
}

// cors answers preflights and tags cross-origin responses. A request without
// an Origin header is left completely untouched.
func cors(config corsConfig) echo.MiddlewareFunc {
	allowMethods := strings.ReplaceAll(config.AllowMethods, " ", "")
	allowHeaders := strings.ReplaceAll(config.AllowHeaders, " ", "")
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get(echo.HeaderOrigin) == "" {
				return next(c)
			}
			header := c.Response().Header()
			// An OPTIONS without Access-Control-Request-Method is outside the
			// CORS protocol: it is tagged Vary and then routed normally, so it
			// can still come back 404, 405 or 401.
			if stringValue(c, ctxOriginalMethod) == http.MethodOptions &&
				c.Request().Header.Get(echo.HeaderAccessControlRequestMethod) == "" {
				header.Set(echo.HeaderVary, echo.HeaderOrigin)
				return next(c)
			}
			if stringValue(c, ctxOriginalMethod) != http.MethodOptions {
				header.Set(echo.HeaderAccessControlAllowOrigin, config.AllowOrigins)
				return next(c)
			}
			header.Set(echo.HeaderVary, strings.Join([]string{
				echo.HeaderAccessControlRequestMethod,
				echo.HeaderAccessControlRequestHeaders,
				echo.HeaderOrigin,
			}, ", "))
			header.Set(echo.HeaderAccessControlAllowOrigin, config.AllowOrigins)
			header.Set(echo.HeaderAccessControlAllowMethods, allowMethods)
			header.Set(echo.HeaderAccessControlAllowHeaders, allowHeaders)
			return c.NoContent(http.StatusNoContent)
		}
	}
}

// ErrorHandler renders framework errors as plain text. The guards reproduce
// the prefix-scoped middleware that runs before the router reports a missing
// route, so a rejected credential is reported instead of the 404 or 405.
func ErrorHandler(guards ...Guard) echo.HTTPErrorHandler {
	var handle echo.HTTPErrorHandler
	handle = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		c.Response().Header().Del(echo.HeaderAllow)
		code := http.StatusInternalServerError
		message := http.StatusText(http.StatusInternalServerError)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			write(c, code, message)
			return
		}
		code = he.Code
		message = messageOf(he)

		if c.Get(ctxFinalError) != nil {
			write(c, code, message)
			return
		}

		switch code {
		case http.StatusNotFound:
			if answered, guardErr := runGuards(c, guards); answered {
				if guardErr != nil {
					handle(guardErr, c)
				}
				return
			}
			if allow := allowHeader(c); allow != "" {
				code = http.StatusMethodNotAllowed
				message = http.StatusText(http.StatusMethodNotAllowed)
				c.Response().Header().Set(echo.HeaderAllow, allow)
				break
			}
			message = "Cannot " + stringValue(c, ctxOriginalMethod) + " " +
				html.EscapeString(stringValue(c, ctxOriginalPath))
		case http.StatusMethodNotAllowed:
			if answered, guardErr := runGuards(c, guards); answered {
				if guardErr != nil {
					handle(guardErr, c)
				}
				return
			}
			c.Response().Header().Set(echo.HeaderAllow, allowHeader(c))
		}
		write(c, code, message)
	}
	return handle
}

// runGuards runs the prefix-scoped middleware for this path and reports whether
// it answered the request. A guard answers either by returning an error or, as
// the credential guard does, by writing the response itself, so a committed
// response counts as answered and must stop the caller writing a second body.
func runGuards(c echo.Context, guards []Guard) (bool, error) {
	path := strings.ToLower(stringValue(c, ctxMatchPath))
	pass := func(echo.Context) error { return nil }
	for _, guard := range guards {
		if !strings.HasPrefix(path, guard.Prefix) {
			continue
		}
		if err := guard.Middleware(pass)(c); err != nil {
			return true, err
		}
		if c.Response().Committed {
			return true, nil
		}
	}
	return false, nil
}

// allowHeader lists, in the documented order, the methods a route was
// registered under for this path. It is derived from the route table rather
// than from the router's own allow set, which advertises OPTIONS everywhere
// and goes empty on a path whose node carries child routes.
func allowHeader(c echo.Context) string {
	path := strings.ToLower(stringValue(c, ctxMatchPath))
	present := make(map[string]bool, len(methodOrder))
	for _, route := range c.Echo().Routes() {
		if matchesPattern(route.Path, path) {
			present[route.Method] = true
		}
	}
	ordered := make([]string, 0, len(present))
	for _, method := range methodOrder {
		if present[method] {
			ordered = append(ordered, method)
		}
	}
	return strings.Join(ordered, ", ")
}

func matchesPattern(pattern, path string) bool {
	patternParts := strings.Split(strings.ToLower(pattern), "/")
	pathParts := strings.Split(path, "/")
	for i, part := range patternParts {
		if strings.HasPrefix(part, "*") {
			return true
		}
		if i >= len(pathParts) {
			return false
		}
		if strings.HasPrefix(part, ":") {
			if pathParts[i] == "" {
				return false
			}
			continue
		}
		if part != pathParts[i] {
			return false
		}
	}
	return len(patternParts) == len(pathParts)
}

func messageOf(he *echo.HTTPError) string {
	if text, ok := he.Message.(string); ok {
		return text
	}
	if he.Message == nil {
		return http.StatusText(he.Code)
	}
	if text, ok := he.Message.(error); ok {
		return text.Error()
	}
	return http.StatusText(he.Code)
}

// SendError writes a framework error exactly as the router's own error handler
// would, and reports success. The baseline's credential guard answers this way:
// its error handler writes the response itself and returns no error, so the
// access log records `-` in the error column for a rejected credential even
// though the client was told why. Returning an error here instead would put the
// message in the log, which the baseline never does.
func SendError(c echo.Context, code int, message string) error {
	c.Set(ctxGuardAnswered, true)
	write(c, code, message)
	return nil
}

func write(c echo.Context, code int, message string) {
	c.Set(ctxLogMessage, message)
	response := c.Response()
	response.Header().Set(echo.HeaderContentType, "text/plain; charset=utf-8")
	response.Header().Set(echo.HeaderContentLength, strconv.Itoa(len(message)))
	response.WriteHeader(code)
	_, _ = response.Write([]byte(message))
}

// OriginalMethod reports the method the request arrived with.
func OriginalMethod(c echo.Context) string {
	return stringValue(c, ctxOriginalMethod)
}

// OriginalPath reports the path the request arrived with.
func OriginalPath(c echo.Context) string {
	return stringValue(c, ctxOriginalPath)
}

func stringValue(c echo.Context, key string) string {
	value, _ := c.Get(key).(string)
	return value
}
