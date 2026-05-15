package response

import (
	"encoding/json"
	"net/http"
)

type ErrorDetail struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type Meta struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	Total    int `json:"total"`
}

type Response struct {
	Data  any          `json:"data"`
	Meta  any          `json:"meta"`
	Error *ErrorDetail `json:"error"`
}

func Success(w http.ResponseWriter, statusCode int, data any) {
	if data == nil {
		data = map[string]any{}
	}
	writeJSON(w, statusCode, Response{Data: data, Meta: nil, Error: nil})
}

func SuccessWithMeta(w http.ResponseWriter, statusCode int, data any, meta Meta) {
	if data == nil {
		data = []any{}
	}
	writeJSON(w, statusCode, Response{Data: data, Meta: meta, Error: nil})
}

func Fail(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, Response{
		Data:  map[string]any{},
		Meta:  nil,
		Error: &ErrorDetail{Message: message},
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, response Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}
