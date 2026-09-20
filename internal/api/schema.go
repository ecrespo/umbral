package api

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// The protocol schema of REQ-API-004 and API Spec §5.37.
//
// It is generated from the Go types the daemon actually serves with, not written beside
// them. A hand-written schema is a third copy of the contract — after the specification and
// the code — and the one nobody updates; `tools/api_schema_check.py` compares this document
// against `specs/api/umbral-daemon-api-v1.md` on every CI run, so a method or an error code
// that exists in one and not the other fails the build instead of being discovered by a
// client.
//
// What the check treats as blocking is method names, required parameters and error codes.
// Descriptions and added optional fields are not: they move for reasons that are not drift.

// SchemaDocument is the whole protocol as this binary speaks it.
type SchemaDocument struct {
	ProtocolVersion int                  `json:"protocol_version"`
	Methods         []SchemaMethod       `json:"methods"`
	Notifications   []SchemaNotification `json:"notifications"`
	Errors          []SchemaError        `json:"errors"`
}

// SchemaMethod is one entry of §5.
type SchemaMethod struct {
	Name string `json:"name"`
	// ClientKinds is the §2 allowlist. An empty list means every kind may call it.
	ClientKinds []string `json:"client_kinds"`
	// Served is false for a method this build knows by name but cannot run, which answers
	// NOT_IMPLEMENTED (§9). The name is published either way: a client comparing schemas
	// across daemons needs to see that the protocol has the method and this build lacks
	// the module.
	Served bool          `json:"served"`
	Params *SchemaObject `json:"params"`
	Result *SchemaObject `json:"result"`
}

// SchemaNotification is one entry of §6.
type SchemaNotification struct {
	Method string        `json:"method"`
	Params *SchemaObject `json:"params"`
}

// SchemaError is one row of §3's error table.
type SchemaError struct {
	DomainCode string `json:"domain_code"`
	Code       int    `json:"code"`
}

// SchemaObject is a JSON Schema object: the subset the protocol uses. Nothing here needs
// `oneOf` or `$ref`, and a generator that emitted them would be describing a language
// rather than this contract.
type SchemaObject struct {
	Type       string                `json:"type"`
	Properties map[string]SchemaProp `json:"properties,omitempty"`
	Required   []string              `json:"required"`
	Items      *SchemaObject         `json:"items,omitempty"`
}

// SchemaProp is one member of an object.
type SchemaProp struct {
	Type string `json:"type"`
	// Items describes the element of an array member.
	Items *SchemaObject `json:"items,omitempty"`
	// Properties describes a nested object member.
	Properties map[string]SchemaProp `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
	// Nullable marks a member the daemon may send as null. §5.3's `focused` is the
	// reason this exists: a member that is present and null is not an absent member.
	Nullable bool `json:"nullable,omitempty"`
}

// optionalTag marks a parameter the caller may omit.
//
// A pointer or an `omitempty` already says so, and both are honoured below. This tag is for
// the rest: a plain `string` or `int` the specification writes as `name?`, where Go has no
// zero value that means "absent". It is read only by the generator, so a field that is
// optional in §5 and untagged here is caught by the CI comparison rather than by a client.
const optionalTag = "optional"

// requiredTag overrides the pointer rule.
//
// A pointer usually means "may be absent", and for the protocol's parameters it usually
// does. §5.8's `root` is the exception: it is a pointer because a tree node is one, and
// `layout.apply` without a tree has nothing to apply. Without the override the published
// schema would invite a call the daemon refuses.
const requiredTag = "required"

// typeObject is the JSON Schema type of every object this generator emits.
const typeObject = "object"

// Schema returns the protocol document. `cfg` decides only which methods are marked served;
// the method set, the parameter shapes and the error table are the same in every build.
func Schema(cfg Config) SchemaDocument {
	doc := SchemaDocument{
		ProtocolVersion: ProtocolVersion,
		Methods:         schemaMethods(cfg),
		Notifications:   schemaNotifications(),
		Errors:          schemaErrors(),
	}
	return doc
}

func schemaErrors() []SchemaError {
	out := make([]SchemaError, 0, len(errorCodes))
	for name, code := range errorCodes {
		out = append(out, SchemaError{DomainCode: name, Code: code})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DomainCode < out[j].DomainCode })
	return out
}

// schemaMethods reflects the registry. The server is built with the caller's config so
// `served` is honest, and with no listener: nothing here touches the socket.
func schemaMethods(cfg Config) []SchemaMethod {
	s := &Server{cfg: cfg}
	table := s.registry()

	out := make([]SchemaMethod, 0, len(table))
	for name, m := range table {
		entry := SchemaMethod{
			Name:        name,
			ClientKinds: kindNames(m.kinds),
			Served:      m.served(cfg),
			Params:      objectSchema(m.params),
			Result:      objectSchema(m.result),
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// notificationShapes is §6's table: every notification this build emits, with the Go type
// of its payload.
//
// Unlike the method shapes it is a table of its own, because a notification has no registry
// entry to hang from — `toNotification` is a type switch over bus events, and the bus events
// are not the wire. TestEveryNotificationDeclaresItsShape_REQ_API_004 walks that switch's
// output and fails when a method it can emit is missing here, which is the same guarantee
// the method table gets from its required fields.
var notificationShapes = map[string]any{
	"session.output":       sessionOutputPayload{},
	"session.resized":      sessionResizedPayload{},
	"session.exited":       sessionExitedPayload{},
	"session.integration":  sessionIntegrationPayload{},
	"session.input_owner":  sessionInputOwnerPayload{},
	"session.unsubscribed": sessionUnsubscribedPayload{},

	"block.started": Block{},
	"block.updated": blockUpdatedPayload{},
	"block.closed":  Block{},

	// §6 types these as the object itself, exactly as `block.started` carries a `Block`.
	"workspace.created": Workspace{},
	"workspace.updated": Workspace{},
	"workspace.closed":  Workspace{},
	"workspace.focused": Workspace{},
	"tab.created":       Tab{},
	"tab.closed":        Tab{},
	"tab.focused":       Tab{},
	"pane.created":      Pane{},
	"pane.updated":      Pane{},
	"pane.closed":       Pane{},
	"pane.focused":      Pane{},
	"pane.moved":        paneMovedPayload{},
	"layout.updated":    Layout{},
}

func schemaNotifications() []SchemaNotification {
	out := make([]SchemaNotification, 0, len(notificationShapes))
	for method, payload := range notificationShapes {
		out = append(out, SchemaNotification{Method: method, Params: objectSchema(payload)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

func kindNames(kinds []ClientKind) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// objectSchema turns a Go struct into the object schema of its JSON form.
//
// A nil shape means the method takes or returns `{}` — §5 types every params and every
// result as an object, so the schema says so rather than emitting null.
func objectSchema(shape any) *SchemaObject {
	obj := &SchemaObject{Type: typeObject, Required: []string{}}
	if shape == nil {
		return obj
	}

	t := reflect.TypeOf(shape)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return obj
	}

	props, required := structProps(t, map[reflect.Type]bool{})
	if len(props) > 0 {
		obj.Properties = props
	}
	obj.Required = required
	return obj
}

// structProps walks one struct's exported fields.
//
// `open` holds the struct types currently being expanded, because the protocol has a
// recursive one: a `Layout`'s root is a `Node`, and a `Node`'s children are `Node`s. A
// member whose type is already open is emitted as a bare object rather than expanded again,
// which is where a JSON Schema would put a `$ref` — the shape is already in the document,
// one level up, and repeating it does not terminate.
func structProps(t reflect.Type, open map[reflect.Type]bool) (map[string]SchemaProp, []string) {
	props := map[string]SchemaProp{}
	required := []string{}

	if open[t] {
		return props, required
	}
	open[t] = true
	defer delete(open, t)

	for i := range t.NumField() {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}

		name, opts, ok := jsonName(field)
		if !ok {
			continue
		}

		// An embedded struct with no JSON name of its own contributes its own members,
		// which is what `blockResult`'s embedded `Block` does on the wire.
		if field.Anonymous && field.Tag.Get("json") == "" {
			inner, innerRequired := structProps(field.Type, open)
			for k, v := range inner {
				props[k] = v
			}
			required = append(required, innerRequired...)
			continue
		}

		prop, nullable := propSchema(field.Type, open)
		prop.Nullable = nullable
		props[name] = prop

		if !optional(field, opts) {
			required = append(required, name)
		}
	}

	sort.Strings(required)
	return props, required
}

// jsonName reads a field's wire name. A field tagged `json:"-"` is not on the wire and is
// not in the schema.
func jsonName(field reflect.StructField) (name, opts string, ok bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", "", false
	}
	name, opts, _ = strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	return name, opts, true
}

// optional reports whether a caller may leave the member out.
//
// Three things say so and they are not redundant: a pointer distinguishes absent from zero,
// `omitempty` means the daemon itself may not write it, and the `api:"optional"` tag covers
// the members §5 writes as `name?` where Go's zero value already means "not given".
func optional(field reflect.StructField, jsonOpts string) bool {
	switch field.Tag.Get("api") {
	case requiredTag:
		return false
	case optionalTag:
		return true
	}
	if field.Type.Kind() == reflect.Pointer {
		return true
	}
	return strings.Contains(jsonOpts, "omitempty")
}

// propSchema maps a Go type onto a JSON type. The second return says whether the daemon may
// send the member as null.
func propSchema(t reflect.Type, open map[reflect.Type]bool) (SchemaProp, bool) {
	nullable := false
	for t.Kind() == reflect.Pointer {
		// A pointer member is one the daemon may write as null — §5.3's `focused` members
		// and §4's `thread_id` are the cases that matter.
		nullable = true
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		return SchemaProp{Type: "string"}, nullable
	case reflect.Bool:
		return SchemaProp{Type: "boolean"}, nullable
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		// Art. 6: every timestamp is epoch ms and every cost micro-USD, both integers.
		// Nothing on this wire is a float.
		return SchemaProp{Type: "integer"}, nullable
	case reflect.Float32, reflect.Float64:
		return SchemaProp{Type: "number"}, nullable
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			// []byte marshals as a base64 string, never as an array of numbers.
			return SchemaProp{Type: "string"}, nullable
		}
		elem := elementSchema(t.Elem(), open)
		return SchemaProp{Type: "array", Items: elem}, true
	case reflect.Map:
		return SchemaProp{Type: typeObject}, nullable
	case reflect.Struct:
		props, required := structProps(t, open)
		return SchemaProp{Type: typeObject, Properties: props, Required: required}, nullable
	case reflect.Interface:
		// `threads: []any` until F1. The member's type is deliberately unconstrained.
		return SchemaProp{}, nullable
	default:
		return SchemaProp{}, nullable
	}
}

func elementSchema(t reflect.Type, open map[reflect.Type]bool) *SchemaObject {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		prop, _ := propSchema(t, open)
		return &SchemaObject{Type: prop.Type, Required: []string{}}
	}
	props, required := structProps(t, open)
	return &SchemaObject{Type: typeObject, Properties: props, Required: required}
}

// schemaResult is `api.schema`'s response (§5.37).
type schemaResult struct {
	Schema SchemaDocument `json:"schema"`
}

// handleAPISchema serves §5.37. It needs no module behind it: the document describes the
// binary, so a daemon can always answer it, and a client that cannot ask what the daemon
// speaks is left guessing.
func handleAPISchema(_ context.Context, c *conn, _ json.RawMessage) (any, error) {
	return schemaResult{Schema: Schema(c.server.cfg)}, nil
}
