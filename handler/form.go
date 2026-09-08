package handler

import (
	"errors"
	"mime"
	"mime/multipart"
	"reflect"
	"strconv"
	"strings"
)

// errBadMultipart is the message the baseline's HTTP server produces when a
// multipart/form-data request carries no usable boundary parameter.
var errBadMultipart = errors.New(
	"request Content-Type has bad boundary or is not multipart/form-data")

// decodeFormValue decodes one application/x-www-form-urlencoded byte run the
// way the baseline's URI decoder does: `+` becomes a space, a well formed
// percent escape is decoded, and a MALFORMED percent escape is passed through
// literally instead of failing the whole request. net/url rejects the request
// outright in that case, which the baseline never does.
func decodeFormValue(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '+':
			out.WriteByte(' ')
		case '%':
			if i+2 >= len(src) {
				out.WriteString(src[i:])
				i = len(src)
				continue
			}
			hi, hiOK := hexDigit(src[i+1])
			lo, loOK := hexDigit(src[i+2])
			if !hiOK || !loOK {
				out.WriteByte('%')
				continue
			}
			out.WriteByte(hi<<4 | lo)
			i += 2
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// parseURLEncoded splits a form body into its key/value pairs without ever
// returning an error, mirroring the baseline's tolerant parser.
func parseURLEncoded(body string) map[string][]string {
	values := make(map[string][]string)
	for _, pair := range strings.Split(body, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		k := decodeFormValue(key)
		if k == "" {
			continue
		}
		values[k] = append(values[k], decodeFormValue(value))
	}
	return values
}

// multipartValues reads a multipart body into the same shape.
func multipartValues(body []byte, contentType string) (map[string][]string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, errBadMultipart
	}
	boundary, ok := params["boundary"]
	if !ok {
		return nil, errBadMultipart
	}
	form, err := multipart.NewReader(strings.NewReader(string(body)), boundary).
		ReadForm(32 << 20)
	if err != nil {
		return nil, errBadMultipart
	}
	values := make(map[string][]string, len(form.Value))
	for key, vals := range form.Value {
		values[key] = vals
	}
	return values, nil
}

// flattenBrackets rewrites `user[email]` into `user.email` so a bracketed form
// key addresses a nested struct field, which is how the baseline binds forms.
func flattenBrackets(key string) string {
	open := strings.IndexByte(key, '[')
	if open < 0 {
		return key
	}
	var out strings.Builder
	out.WriteString(key[:open])
	rest := key[open:]
	for len(rest) > 0 {
		if rest[0] != '[' {
			return key
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return key
		}
		out.WriteByte('.')
		out.WriteString(rest[1:end])
		rest = rest[end+1:]
	}
	return out.String()
}

// bindFormValues assigns form values onto out by walking `a.b.c` paths against
// the struct's field names, matched case insensitively. Unknown keys are
// ignored rather than reported, which is what the baseline's decoder does.
func bindFormValues(out interface{}, values map[string][]string) {
	target := reflect.ValueOf(out)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return
	}
	for key, vals := range values {
		if len(vals) == 0 {
			continue
		}
		assignPath(target.Elem(), strings.Split(flattenBrackets(key), "."), vals)
	}
}

func assignPath(target reflect.Value, path []string, values []string) {
	for _, name := range path {
		if target.Kind() != reflect.Struct {
			return
		}
		field := fieldByName(target, name)
		if !field.IsValid() || !field.CanSet() {
			return
		}
		target = field
	}
	setFieldValue(target, values)
}

func fieldByName(target reflect.Value, name string) reflect.Value {
	t := target.Type()
	for i := 0; i < t.NumField(); i++ {
		if strings.EqualFold(t.Field(i).Name, name) {
			return target.Field(i)
		}
	}
	return reflect.Value{}
}

func setFieldValue(field reflect.Value, values []string) {
	if field.Kind() == reflect.Slice {
		slice := reflect.MakeSlice(field.Type(), len(values), len(values))
		for i, v := range values {
			setScalar(slice.Index(i), v)
		}
		field.Set(slice)
		return
	}
	setScalar(field, values[len(values)-1])
}

func setScalar(field reflect.Value, value string) {
	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
	case reflect.Bool:
		if parsed, err := strconv.ParseBool(value); err == nil {
			field.SetBool(parsed)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			field.SetInt(parsed)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if parsed, err := strconv.ParseUint(value, 10, 64); err == nil {
			field.SetUint(parsed)
		}
	case reflect.Float32, reflect.Float64:
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			field.SetFloat(parsed)
		}
	}
}
