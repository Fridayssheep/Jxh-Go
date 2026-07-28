package adminapi

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	writeJSON(w, status, value)
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, fields map[string][]string, retryable bool) {
	writeAPIError(w, r, status, code, message, fields, retryable)
}
