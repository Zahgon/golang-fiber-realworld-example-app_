package handler

import (
	"net/http"
	"strings"

	"github.com/alpody/echo-realworld/router"
	"github.com/alpody/echo-realworld/utils"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// authScheme is the Authorization scheme the credential must carry, with the
// single separating space the baseline's extractor requires.
const authScheme = "Token "

// extractToken reproduces the baseline's Authorization parsing exactly. The
// length test is STRICTLY GREATER THAN the scheme plus one, so `Token x` is
// reported as malformed rather than as a bad credential, and whatever follows
// the scheme is space trimmed, so `Token  <jwt>` still authenticates.
func extractToken(c echo.Context) (string, bool) {
	auth := c.Request().Header.Get(echo.HeaderAuthorization)
	if len(auth) > len(authScheme)+1 &&
		strings.EqualFold(auth[:len(authScheme)], authScheme) {
		return strings.TrimSpace(auth[len(authScheme):]), true
	}
	return "", false
}

// jwtMiddlewareWith builds the credential guard. The signing key is accepted
// for any algorithm the key itself supports, which is what the baseline does:
// it pins no algorithm, so a token signed HS384 or HS512 with the same secret
// is valid there and must be valid here.
func jwtMiddlewareWith(skipper func(echo.Context) bool) echo.MiddlewareFunc {
	keyFunc := func(token *jwt.Token) (interface{}, error) { return utils.JWTSecret, nil }
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if skipper != nil && skipper(c) {
				return next(c)
			}
			raw, ok := extractToken(c)
			if !ok {
				return router.SendError(c, http.StatusBadRequest, "missing or malformed JWT")
			}
			token, err := jwt.Parse(raw, keyFunc, jwt.WithValidMethods(hmacMethods))
			if err != nil || !token.Valid {
				return router.SendError(c, http.StatusUnauthorized, "Invalid or expired JWT")
			}
			c.Set("user", token)
			return next(c)
		}
	}
}

var hmacMethods = []string{"HS256", "HS384", "HS512"}

// Every GET below is paired with a HEAD on the same path because Fiber's
// Group.Get registered both; the pairs are spelled out rather than wrapped so
// the route table states the whole contract to anyone reading or scanning it.
func (h *Handler) Register(r *echo.Echo) {
	v1 := r.Group("/api")
	jwtMiddleware := jwtMiddlewareWith(nil)
	//v1.Use(jwtMiddleware)
	guestUsers := v1.Group("/users")
	guestUsers.POST("", h.SignUp)
	guestUsers.POST("/login", h.Login)
	user := v1.Group("/user", jwtMiddleware)
	user.GET("", h.CurrentUser)
	user.HEAD("", h.CurrentUser)
	user.PUT("", h.UpdateUser)

	profiles := v1.Group("/profiles", jwtMiddleware)
	profiles.GET("/:username", h.GetProfile)
	profiles.HEAD("/:username", h.GetProfile)

	profiles.POST("/:username/follow", h.Follow)
	profiles.DELETE("/:username/follow", h.Unfollow)

	articlesJWTMiddleware := jwtMiddlewareWith(func(c echo.Context) bool {
		return router.OriginalMethod(c) == "GET" &&
			router.OriginalPath(c) != "/api/articles/feed"
	})
	articles := v1.Group("/articles", articlesJWTMiddleware)
	articles.POST("", h.CreateArticle)
	articles.GET("/feed", h.Feed)
	articles.HEAD("/feed", h.Feed)
	articles.PUT("/:slug", h.UpdateArticle)
	articles.DELETE("/:slug", h.DeleteArticle)
	articles.POST("/:slug/comments", h.AddComment)
	articles.DELETE("/:slug/comments/:id", h.DeleteComment)
	articles.POST("/:slug/favorite", h.Favorite)
	articles.DELETE("/:slug/favorite", h.Unfavorite)
	articles.GET("", h.Articles)
	articles.HEAD("", h.Articles)
	articles.GET("/:slug", h.GetArticle)
	articles.HEAD("/:slug", h.GetArticle)
	articles.GET("/:slug/comments", h.GetComments)
	articles.HEAD("/:slug/comments", h.GetComments)

	tags := v1.Group("/tags")
	tags.GET("", h.Tags)
	tags.HEAD("", h.Tags)

	// The guards below are mounted by path prefix, so they also answer
	// requests that match no route at all.
	r.HTTPErrorHandler = router.ErrorHandler(
		router.Guard{Prefix: "/api/user", Middleware: jwtMiddleware},
		router.Guard{Prefix: "/api/profiles", Middleware: jwtMiddleware},
		router.Guard{Prefix: "/api/articles", Middleware: articlesJWTMiddleware},
	)
}
