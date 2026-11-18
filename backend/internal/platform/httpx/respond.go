// Package httpx holds the HTTP response envelope, error mapping and
// pagination shared by every API handler.
package httpx

import (
	"encoding/json"
	"net/http"
)

type envelope struct {
	Data any `json:"data"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// JSON writes v as {"data": v} with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	write(w, status, envelope{Data: v})
}

// Fail writes {"error": {code, message, details}} with the given status.
func Fail(w http.ResponseWriter, status int, code, message string, details any) {
	write(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Details: details}})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
