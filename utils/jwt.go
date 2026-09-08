package utils

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var secretString = []byte("!!SECRET!!")
var JWTSecret = secretString

func GenerateJWT(id uint) string {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)
	claims["id"] = id
	claims["exp"] = time.Now().Add(time.Hour * 72).Unix()
	t, _ := token.SignedString(secretString)
	return t
}
