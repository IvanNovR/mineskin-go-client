package mineskin

import "encoding/json"

// ResponseMeta describes the envelope of a MineSkin API response.
type ResponseMeta struct {
	// Status is the HTTP status code.
	Status int
	// Success is the value of the response's success field, or whether the
	// status is 200 when the field is absent.
	Success bool
	// Errors lists the errors reported by the API.
	Errors []CodeAndMessage
	// Warnings lists the warnings reported by the API.
	Warnings []CodeAndMessage
	// Messages lists the informational messages reported by the API.
	Messages []CodeAndMessage
	// Headers holds the mineskin-* and x-mineskin-* response headers, keyed
	// by lower-case name.
	Headers map[string]string
	// Server is the value of the mineskin-server header.
	Server string
	// Breadcrumb is the value of the mineskin-breadcrumb header.
	Breadcrumb string
	// Links is the raw links field of the response, or nil when absent.
	Links json.RawMessage
}
