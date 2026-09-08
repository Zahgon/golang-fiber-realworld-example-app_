package handler

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/alpody/echo-realworld/model"
	"github.com/gosimple/slug"
	"github.com/labstack/echo/v4"
)

var errUnprocessableEntity = errors.New("Unprocessable Entity")

// parseVendorContentType rewrites a vendor media type onto the base type it
// extends, so `application/vnd.api+json` is read as `application/json`.
func parseVendorContentType(ctype string) string {
	plus := strings.Index(ctype, "+")
	if plus == -1 {
		return ctype
	}
	var parsable string
	semi := strings.Index(ctype, ";")
	switch {
	case semi == -1:
		parsable = ctype[plus+1:]
	case plus < semi:
		parsable = ctype[plus+1 : semi]
	default:
		return ctype[:semi]
	}
	slash := strings.Index(ctype, "/")
	if slash == -1 {
		return ctype
	}
	return ctype[:slash+1] + parsable
}

// bodyParser fills out from the request body using the baseline's media type
// dispatch: any type whose name ends in `json` is decoded as JSON, the two form
// types are decoded as forms, and anything else is an unprocessable entity.
// The content type is NOT trimmed before the `;` is cut, so a space before the
// parameter list (`application/json ; charset=utf-8`) is unparsable, exactly as
// in the baseline.
func bodyParser(c echo.Context, out interface{}) error {
	ctype := strings.ToLower(c.Request().Header.Get(echo.HeaderContentType))
	ctype = parseVendorContentType(ctype)
	if end := strings.IndexByte(ctype, ';'); end != -1 {
		ctype = ctype[:end]
	}

	switch {
	case strings.HasSuffix(ctype, "json"):
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return json.Unmarshal(body, out)
	case ctype == echo.MIMEApplicationForm:
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		bindFormValues(out, parseURLEncoded(string(body)))
		return nil
	case ctype == echo.MIMEMultipartForm:
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		values, err := multipartValues(body, c.Request().Header.Get(echo.HeaderContentType))
		if err != nil {
			return err
		}
		bindFormValues(out, values)
		return nil
	default:
		return errUnprocessableEntity
	}
}

type userUpdateRequest struct {
	User struct {
		Username string `json:"username"`
		Email    string `json: "email" validate:"email"`
		Password string `json:"password"`
		Bio      string `json:"bio"`
		Image    string `json:"image"`
	} `json:"user"`
}

func newUserUpdateRequest() *userUpdateRequest {
	return new(userUpdateRequest)
}
func (r *userUpdateRequest) populate(u *model.User) {
	r.User.Username = u.Username
	r.User.Email = u.Email
	r.User.Password = u.Password
	if u.Bio != nil {
		r.User.Bio = *u.Bio
	}
	if u.Image != nil {
		r.User.Image = *u.Image
	}
}

func (r *userUpdateRequest) bind(c echo.Context, u *model.User, v *Validator) error {
	if err := bodyParser(c, r); err != nil {
		return err
	}
	if err := v.Validate(r); err != nil {
		return err
	}
	u.Username = r.User.Username
	u.Email = r.User.Email
	//fmt.Printf("request user %v, from db user %v", r.User, *u)
	if r.User.Password != u.Password {
		h, err := u.HashPassword(r.User.Password)
		if err != nil {
			return err
		}
		u.Password = h

	}
	u.Bio = &r.User.Bio
	u.Image = &r.User.Image
	return nil
}

type userRegisterRequest struct {
	User struct {
		Username string `json:"username" validate:"required"`
		Email    string `json: "email" validate:"required, email"`
		Password string `json:"password" validate:"required"`
	} `json:"user"`
}

func (r *userRegisterRequest) bind(c echo.Context, u *model.User, v *Validator) error {
	//validate

	if err := bodyParser(c, r); err != nil {
		return err
	}
	//fmt.Printf("%v", *r)

	if err := v.Validate(r); err != nil {
		return err
	}
	u.Username = r.User.Username
	u.Email = r.User.Email
	h, err := u.HashPassword(r.User.Password)
	if err != nil {
		return err
	}
	u.Password = h
	return nil
}

type userLoginRequest struct {
	User struct {
		Email    string `json:"email" validate:"required,email"`
		Password string `json:"password" validate: "required"`
	} `json:"user"`
}

func (r *userLoginRequest) bind(c echo.Context, v *Validator) error {

	if err := bodyParser(c, r); err != nil {
		return err
	}

	if err := v.Validate(r); err != nil {
		return err
	}
	//fmt.Printf("%v", *r)
	return nil
}

type articleCreateRequest struct {
	Article struct {
		Title       string   `json:"title" validate:"required"`
		Description string   `json:"description" validate:"required"`
		Body        string   `json:"body" validate:"required"`
		Tags        []string `json:"tagList, omitempty"`
	} `json:"article"`
}

func (r *articleCreateRequest) bind(c echo.Context, a *model.Article, v *Validator) error {
	if err := bodyParser(c, r); err != nil {
		return err
	}
	if err := v.Validate(r); err != nil {
		return err
	}
	a.Title = r.Article.Title
	a.Slug = slug.Make(r.Article.Title)
	a.Description = r.Article.Description
	a.Body = r.Article.Body
	if r.Article.Tags != nil {
		for _, t := range r.Article.Tags {
			a.Tags = append(a.Tags, model.Tag{Tag: t})
		}
	}
	return nil
}

type articleUpdateRequest struct {
	Article struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Body        string   `json:"body"`
		Tags        []string `json:"tagList"`
	} `json:"article"`
}

func (r *articleUpdateRequest) populate(a *model.Article) {
	r.Article.Title = a.Title
	r.Article.Description = a.Description
	r.Article.Body = a.Body
}

func (r *articleUpdateRequest) bind(c echo.Context, a *model.Article, v *Validator) error {
	if err := bodyParser(c, r); err != nil {
		return err
	}
	if err := v.Validate(r); err != nil {
		return err
	}
	a.Title = r.Article.Title
	a.Slug = slug.Make(a.Title)
	a.Description = r.Article.Description
	a.Body = r.Article.Body
	return nil
}

type createCommentRequest struct {
	Comment struct {
		Body string `json:"body" validate:"required"`
	} `json:"comment"`
}

func (r *createCommentRequest) bind(c echo.Context, cm *model.Comment, v *Validator) error {
	if err := bodyParser(c, r); err != nil {
		return err
	}
	if err := v.Validate(r); err != nil {
		return err
	}
	cm.Body = r.Comment.Body
	cm.UserID = userIDFromToken(c)
	return nil
}
