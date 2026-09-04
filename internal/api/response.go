package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// check turns a generated response into an error when the status is not 2xx. The typed JSONxxx
// field is left to the caller: this only decides success or failure.
//
// It takes the body separately because the generated types expose it as a plain field rather
// than through an interface.
func check(op string, status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return &Error{
		StatusCode: status,
		Status:     http.StatusText(status),
		Message:    errorMessage(body),
		Op:         op,
	}
}

// errorMessage pulls a human-readable message out of an error body. Keycloak and the phasetwo
// module between them use several shapes, so try each and fall back to the raw body.
func errorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err == nil {
		for _, k := range []string{"error", "errorMessage", "error_description", "message"} {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	s := strings.TrimSpace(string(body))
	const max = 512
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// missingBody is returned when the server answered 2xx but the typed payload was absent. That
// means the response did not match the schema the client was generated from — worth surfacing
// loudly rather than proceeding with a zero value.
func missingBody(op string, status int) error {
	return fmt.Errorf("%s: server returned %d with no parseable body; "+
		"the API may not match the vendored OpenAPI spec (see api/SPEC.md)", op, status)
}
