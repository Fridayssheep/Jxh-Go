package adminapi

import "net/http"

const (
	CodeBadRequest           = "invalid_request"
	CodeUnauthorized         = "unauthorized"
	CodeForbidden            = "forbidden"
	CodeConflict             = "conflict"
	CodePreconditionRequired = "precondition_required"
	CodeOriginForbidden      = "origin_forbidden"
	CodeCSRFInvalid          = "csrf_invalid"
	CodeNotFound             = "not_found"
	CodeMethodNotAllowed     = "method_not_allowed"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeInternal             = "internal_error"
	CodeRateLimited          = "rate_limited"
)

type Error struct {
	Code      string              `json:"code"`
	Message   string              `json:"message"`
	RequestID string              `json:"request_id"`
	Fields    map[string][]string `json:"fields"`
	Retryable bool                `json:"retryable"`
}

type ErrorResponse struct {
	Error Error `json:"error"`
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string, fields map[string][]string, retryable bool) {
	if fields == nil {
		fields = map[string][]string{}
	}
	writeJSON(w, status, ErrorResponse{Error: Error{
		Code: code, Message: message, RequestID: RequestIDFromContext(r.Context()), Fields: fields, Retryable: retryable,
	}})
}
