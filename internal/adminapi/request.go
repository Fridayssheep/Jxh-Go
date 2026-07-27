package adminapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zjutjh/jxh-go/internal/auth"
)

var (
	ErrInvalidJSON     = errors.New("invalid JSON request")
	ErrPayloadTooLarge = errors.New("request body too large")
)

type requestContextKey uint8

const (
	requestIDContextKey requestContextKey = iota
	clientIPContextKey
	authContextKey
)

func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

func ClientIPFromContext(ctx context.Context) string {
	value, _ := ctx.Value(clientIPContextKey).(string)
	return value
}

func AuthFromContext(ctx context.Context) (auth.AuthContext, bool) {
	value, ok := ctx.Value(authContextKey).(auth.AuthContext)
	return value, ok
}

func DecodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrPayloadTooLarge
		}
		return ErrInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrPayloadTooLarge
		}
		return ErrInvalidJSON
	}
	return nil
}

func validCSRF(header string, expected string) bool {
	if header == "" || expected == "" || len(header) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}
