package handler

import (
	"net/http"
)

// parseBoundedForm caps the request body and parses the form in one step so
// every POST handler shares the same size bound. Callers keep their
// feature-specific error messages.
func parseBoundedForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	return r.ParseForm()
}
