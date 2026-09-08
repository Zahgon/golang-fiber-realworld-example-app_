// You must first install   https://github.com/swaggo/swag
//
//go:generate swag init
package main

import (
	"fmt"

	"github.com/alpody/echo-realworld/db"
	_ "github.com/alpody/echo-realworld/docs"
	"github.com/alpody/echo-realworld/handler"
	"github.com/alpody/echo-realworld/router"
	"github.com/alpody/echo-realworld/store"
)

// @description Conduit API
// @title Conduit API

// @BasePath /api

// @schemes http https
// @produce application/json
// @consumes application/json

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization

func main() {
	r := router.New()
	// The original registered this with fiber's Group.Get, which binds HEAD to
	// the same handler, and its handler answered the bare prefix as well as the
	// wildcard, so all four registrations are spelled out.
	swagger := router.Swagger("/swagger")
	r.GET("/swagger", swagger)
	r.HEAD("/swagger", swagger)
	r.GET("/swagger/*", swagger)
	r.HEAD("/swagger/*", swagger)
	d := db.New()
	db.AutoMigrate(d)

	us := store.NewUserStore(d)
	as := store.NewArticleStore(d)

	h := handler.NewHandler(us, as)
	h.Register(r)
	err := r.Start(":8585")
	if err != nil {
		fmt.Printf("%v", err)
	}
}
