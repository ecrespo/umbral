package api

import (
	"errors"
	"fmt"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// The `domain_code` strings of API Spec §3, named because more than one module maps its
// sentinels onto them and a typo in one place would ship a code no client recognises.
const (
	domainNotFound        = "NOT_FOUND"
	domainValidationError = "VALIDATION_ERROR"
	domainConflict        = "CONFLICT"
	domainInputLocked     = "INPUT_LOCKED"
)

// Domain error codes from API Spec §3. The JSON-RPC numbers are part of the contract,
// so they live next to the names rather than being computed.
const (
	codeParseError                 = -32700
	codeInvalidRequest             = -32600
	codeMethodNotFound             = -32601
	codeValidationError            = -32602
	codeInternalError              = -32603
	codeUnauthorized               = -32001
	codeNotFound                   = -32002
	codeConflict                   = -32003
	codePermissionDenied           = -32004
	codeProviderUnavailable        = -32005
	codeBudgetExceeded             = -32006
	codeUnsupportedProtocolVersion = -32007
	codeInputLocked                = -32008
	codeConfigInvalid              = -32009
	codeThreadBlocked              = -32010
	codeTimeout                    = -32011
	codeNotImplemented             = -32012
	codeCancelled                  = -32013
)

// errorCodes is the whole §3 table, in one place a generated schema can read.
//
// The constants above are what the handlers use; this map is what `api.schema` publishes
// and what `tools/api_schema_check.py` compares against the specification. Keeping the two
// together means a code added to the daemon without a row in §3 fails the build, which is
// the drift REQ-API-004 exists to catch. Codes for methods F0 does not serve yet are listed
// because the table, not the method set, is the contract: a client switching on
// `domain_code` needs the complete set to be exhaustive.
var errorCodes = map[string]int{
	"PARSE_ERROR":                  codeParseError,
	"INVALID_REQUEST":              codeInvalidRequest,
	"METHOD_NOT_FOUND":             codeMethodNotFound,
	"VALIDATION_ERROR":             codeValidationError,
	"INTERNAL_ERROR":               codeInternalError,
	"UNAUTHORIZED":                 codeUnauthorized,
	"NOT_FOUND":                    codeNotFound,
	"CONFLICT":                     codeConflict,
	"PERMISSION_DENIED":            codePermissionDenied,
	"PROVIDER_UNAVAILABLE":         codeProviderUnavailable,
	"BUDGET_EXCEEDED":              codeBudgetExceeded,
	"UNSUPPORTED_PROTOCOL_VERSION": codeUnsupportedProtocolVersion,
	domainInputLocked:              codeInputLocked,
	"CONFIG_INVALID":               codeConfigInvalid,
	"THREAD_BLOCKED":               codeThreadBlocked,
	"TIMEOUT":                      codeTimeout,
	"NOT_IMPLEMENTED":              codeNotImplemented,
	"CANCELLED":                    codeCancelled,
}

// Sentinel errors the modules return. api is the only package that knows which JSON-RPC
// number each one becomes (Tech Design §5.4), so a handler returns a domain error and
// never a wire code.
var (
	ErrNotFound            = errors.New("not_found")
	ErrConflict            = errors.New("conflict")
	ErrPermissionDenied    = errors.New("permission_denied")
	ErrProviderUnavailable = errors.New("provider_unavailable")
	ErrBudgetExceeded      = errors.New("budget_exceeded")
	ErrInputLocked         = errors.New("input_locked")
	ErrConfigInvalid       = errors.New("config_invalid")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrMethodNotFound      = errors.New("method_not_found")
	ErrUnsupportedProtocol = errors.New("unsupported_protocol_version")
	// ErrNotImplemented is the other half of REQ-API-003: a method this build knows by
	// name but cannot serve, because the module behind it is not wired in. It is not
	// METHOD_NOT_FOUND — that one means "this daemon has never heard of the name" — and
	// the difference is what lets a client tell an old daemon from a partial one
	// (API Spec §9).
	ErrNotImplemented = errors.New("not_implemented")
	ErrThreadBlocked  = errors.New("thread_blocked")
	ErrTimeout        = errors.New("timeout")
	ErrCancelled      = errors.New("cancelled")
)

// apiError is a domain error enriched with the per-field details the contract allows.
// Handlers build one with ValidationError; everything else maps through sentinels.
type apiError struct {
	err     error
	message string
	details []ErrorField
	// supported is set only for an unsupported handshake version.
	supported []int
}

func (e *apiError) Error() string { return e.message }
func (e *apiError) Unwrap() error { return e.err }

// ValidationError reports invalid parameters, naming the offending fields.
func ValidationError(message string, details ...ErrorField) error {
	return &apiError{err: errValidation, message: message, details: details}
}

// errValidation is unexported: callers reach it through ValidationError, which forces
// them to say which field was wrong.
var errValidation = errors.New("validation_error")

// unsupportedProtocolError reports a handshake this daemon cannot serve, listing the
// versions it does accept (API Spec §2).
func unsupportedProtocolError(got int) error {
	return &apiError{
		err:       ErrUnsupportedProtocol,
		message:   fmt.Sprintf("protocol version %d is not supported", got),
		supported: []int{ProtocolVersion},
	}
}

// moduleErrors is every module's sentinel translation, consulted before the generic codes.
//
// It is a list rather than a chain of ifs because forgetting to add a module here is
// invisible: its errors keep reaching clients, just as INTERNAL_ERROR with a trace id
// attached, which is both the wrong code and a leak Art. 7 reserves for real faults. That
// is what happened to the workspace tree until `TestWorkspaceErrorsReachTheWire` was
// written.
var moduleErrors = []func(error) (int, string, bool){
	sessionDomainError,
	workspaceDomainError,
}

// workspaceDomainError maps the workspace tree's sentinels onto the §5.4 table.
func workspaceDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, wsdomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	case errors.Is(err, wsdomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, wsdomain.ErrConflict):
		return codeConflict, domainConflict, true
	default:
		return 0, "", false
	}
}

// toWire translates a handler error into the protocol object of API Spec §3.
//
// traceID is attached to INTERNAL_ERROR only, where Art. 7 requires it: for the other
// codes the client already knows what it did wrong, and a trace id would leak the
// daemon's internals into an ordinary validation message.
func toWire(err error, traceID string) *wireError {
	var domainCode string
	var code int

	// Module sentinels first: they are more specific than the generic ones below, and a
	// module error that also wrapped a generic sentinel must map to its own code.
	for _, translate := range moduleErrors {
		if moduleCode, moduleDomain, ok := translate(err); ok {
			code, domainCode = moduleCode, moduleDomain
			return finishWire(err, code, domainCode, traceID)
		}
	}

	switch {
	case errors.Is(err, errValidation):
		code, domainCode = codeValidationError, domainValidationError
	case errors.Is(err, ErrUnauthorized):
		code, domainCode = codeUnauthorized, "UNAUTHORIZED"
	case errors.Is(err, ErrMethodNotFound):
		code, domainCode = codeMethodNotFound, "METHOD_NOT_FOUND"
	case errors.Is(err, ErrNotImplemented):
		code, domainCode = codeNotImplemented, "NOT_IMPLEMENTED"
	case errors.Is(err, ErrThreadBlocked):
		code, domainCode = codeThreadBlocked, "THREAD_BLOCKED"
	case errors.Is(err, ErrTimeout):
		code, domainCode = codeTimeout, "TIMEOUT"
	case errors.Is(err, ErrCancelled):
		code, domainCode = codeCancelled, "CANCELLED"
	case errors.Is(err, ErrUnsupportedProtocol):
		code, domainCode = codeUnsupportedProtocolVersion, "UNSUPPORTED_PROTOCOL_VERSION"
	case errors.Is(err, ErrNotFound):
		code, domainCode = codeNotFound, domainNotFound
	case errors.Is(err, ErrConflict):
		code, domainCode = codeConflict, domainConflict
	case errors.Is(err, ErrPermissionDenied):
		code, domainCode = codePermissionDenied, "PERMISSION_DENIED"
	case errors.Is(err, ErrProviderUnavailable):
		code, domainCode = codeProviderUnavailable, "PROVIDER_UNAVAILABLE"
	case errors.Is(err, ErrBudgetExceeded):
		code, domainCode = codeBudgetExceeded, "BUDGET_EXCEEDED"
	case errors.Is(err, ErrInputLocked):
		code, domainCode = codeInputLocked, domainInputLocked
	case errors.Is(err, ErrConfigInvalid):
		code, domainCode = codeConfigInvalid, "CONFIG_INVALID"
	default:
		code, domainCode = codeInternalError, "INTERNAL_ERROR"
	}

	return finishWire(err, code, domainCode, traceID)
}

// finishWire assembles the protocol error once its code has been decided.
func finishWire(err error, code int, domainCode, traceID string) *wireError {
	data := &errorData{DomainCode: domainCode}
	var detailed *apiError
	if errors.As(err, &detailed) {
		data.Details = detailed.details
		data.Supported = detailed.supported
	}

	message := err.Error()
	if code == codeInternalError {
		data.TraceID = traceID
		// The client gets the trace id, not the internals. Art. 7 also forbids leaking
		// anything sensitive, and an arbitrary wrapped error is not vetted for that.
		message = "internal error"
	}
	return &wireError{Code: code, Message: message, Data: data}
}
