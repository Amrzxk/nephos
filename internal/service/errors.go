// Package service validates resource requests and commits desired state.
package service

import "fmt"

// Error is a transport-independent, AWS-style resource error.
type Error struct {
	Code       string
	Message    string
	ResourceID string
	Status     int
}

func (e *Error) Error() string {
	if e.ResourceID != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.ResourceID)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func invalidParameter(message string) error {
	return &Error{Code: "InvalidParameterValue", Message: message, Status: 400}
}
