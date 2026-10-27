// Package apperr defines the machine-readable error contract shared by the CLI,
// the HTTP API and skills. Codes are stable; messages are for humans.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Stable error codes. Skills branch on these, so never rename one.
const (
	InvalidInput     = "E_INVALID_INPUT"
	NotFound         = "E_NOT_FOUND"
	Conflict         = "E_CONFLICT"
	RevisionConflict = "E_REVISION_CONFLICT"
	Locked           = "E_LOCKED"
	Validation       = "E_VALIDATION"
	ConfirmRequired  = "E_CONFIRM_REQUIRED"
	HumanOnly        = "E_HUMAN_ONLY"
	Unauthorized     = "E_UNAUTHORIZED"
	NotConfigured    = "E_NOT_CONFIGURED"
	UnsupportedMedia = "E_UNSUPPORTED_MEDIA"
	Integrity        = "E_INTEGRITY"
	Storage          = "E_STORAGE"
	Database         = "E_DATABASE"
	ToolMissing      = "E_TOOL_MISSING"
	ToolFailed       = "E_TOOL_FAILED"
	NoWorker         = "E_NO_WORKER"
	Canceled         = "E_CANCELED"
	Internal         = "E_INTERNAL"
	// UnsafeSecretPath refuses a secret file inside a sync folder or a repository.
	UnsafeSecretPath = "E_UNSAFE_SECRET_PATH"
	// Provider is an error reported by an external provider API (e.g. TTS).
	Provider = "E_PROVIDER"
	// Budget refuses paid work that would exceed a configured budget.
	Budget = "E_BUDGET"
	// ConsentRequired refuses work (voice cloning, someone's likeness in
	// generated video) without a recorded consent statement.
	ConsentRequired = "E_CONSENT_REQUIRED"
	// SafetyBlocked is a provider's safety filters declining a request (a
	// generated video); details say which side and the provider's codes.
	SafetyBlocked = "E_SAFETY_BLOCKED"
	// Held refuses to send a version out while the quality check found
	// problems, hasn't checked it, or a sign-off is waiting, with the gate on
	// (D298); details list what holds it and the command that shows it.
	Held = "E_HELD"
	// KeptHere refuses sending a client's things somewhere their Content
	// Settings keep them from (What may leave, D288); details say the place,
	// the kind, whose settings and who set the rule.
	KeptHere = "E_KEPT_HERE"
)

var statusByCode = map[string]int{
	InvalidInput: http.StatusBadRequest, NotFound: http.StatusNotFound, Conflict: http.StatusConflict,
	RevisionConflict: http.StatusConflict, Locked: http.StatusConflict, Validation: http.StatusUnprocessableEntity,
	ConfirmRequired: http.StatusPreconditionRequired, HumanOnly: http.StatusForbidden, Unauthorized: http.StatusUnauthorized,
	NotConfigured: http.StatusFailedDependency, UnsupportedMedia: http.StatusUnsupportedMediaType, Integrity: http.StatusUnprocessableEntity,
	Storage: http.StatusBadGateway, Database: http.StatusServiceUnavailable, ToolMissing: http.StatusFailedDependency,
	ToolFailed: http.StatusInternalServerError, NoWorker: http.StatusServiceUnavailable, Canceled: 499, Internal: http.StatusInternalServerError,
	UnsafeSecretPath: http.StatusForbidden, Provider: http.StatusBadGateway, Budget: http.StatusPaymentRequired, ConsentRequired: http.StatusForbidden,
	SafetyBlocked: http.StatusUnprocessableEntity, Held: http.StatusConflict, KeptHere: http.StatusForbidden,
}

// Error is the single error shape returned everywhere.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// HTTPStatus maps the code to an HTTP status.
func (e *Error) HTTPStatus() int {
	if s, ok := statusByCode[e.Code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// New builds an error with a code and message.
func New(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause.
func Wrap(code string, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), cause: cause}
}

// WithHint returns e with a remediation hint.
func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }

// WithDetails returns e with structured details (a command kept with
// WithCommand stays when d is a map).
func (e *Error) WithDetails(d any) *Error {
	if cmd := e.Command(); cmd != "" {
		if m, ok := d.(map[string]any); ok {
			if _, has := m["command"]; !has {
				m["command"] = cmd
			}
		}
	}
	e.Details = d
	return e
}

// WithCommand keeps the exact command that fixes the problem (for people at a
// terminal and for Claude Code) in Details["command"], so the message and the
// hint can stay in plain words: the CLI shows it with --details and --json,
// the dashboard under Details. Details that are not a map keep their value
// and get no command.
func (e *Error) WithCommand(cmd string) *Error {
	switch d := e.Details.(type) {
	case nil:
		e.Details = map[string]any{"command": cmd}
	case map[string]any:
		d["command"] = cmd
	}
	return e
}

// Command returns the command kept with WithCommand ("" when none).
func (e *Error) Command() string {
	if d, ok := e.Details.(map[string]any); ok {
		if s, ok := d["command"].(string); ok {
			return s
		}
	}
	return ""
}

// Coded is implemented by errors from packages that cannot build an *Error
// directly but know their stable code (e.g. secretpath's refusals).
type Coded interface{ AppError() *Error }

// From converts any error into *Error (unknown errors become E_INTERNAL).
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	var c Coded
	if errors.As(err, &c) {
		if e := c.AppError(); e != nil {
			return e
		}
	}
	return &Error{Code: Internal, Message: err.Error(), cause: err}
}

// Is reports whether err carries the given code.
func Is(err error, code string) bool {
	var ae *Error
	return errors.As(err, &ae) && ae.Code == code
}
