package regruapi

import (
	"bytes"
	"encoding/json"
	"strings"
)

type APIError struct {
	Code   string
	Text   string
	Params json.RawMessage
}

func (e *APIError) Error() string {
	s := "reg.ru api error " + e.Code
	if e.Text != "" {
		s += ": " + e.Text
	}
	return s
}

type UnknownOutcomeError struct {
	Method string
	Err    error
}

func (e *UnknownOutcomeError) Error() string {
	return "outcome of " + e.Method + " is unknown: " + e.Err.Error()
}

func (e *UnknownOutcomeError) Unwrap() error { return e.Err }

func (e *UnknownOutcomeError) UnknownOutcome() bool { return true }

type TransportError struct {
	Method string
	Err    error
}

func (e *TransportError) Error() string {
	return "transport error in " + e.Method + ": " + e.Err.Error()
}

func (e *TransportError) Unwrap() error { return e.Err }

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Unwrap() error { return e.err }

type Credentials struct {
	Username string
	Password string
}

func (c Credentials) String() string {
	return "username=" + c.Username + " password=***"
}

func (c Credentials) redact(s string) string {
	if c.Password == "" {
		return s
	}
	s = strings.ReplaceAll(s, c.Password, "***")
	if b, err := json.Marshal(c.Password); err == nil && len(b) > 2 {
		s = strings.ReplaceAll(s, string(b[1:len(b)-1]), "***")
	}
	return s
}

func (c Credentials) redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	red := c.redact(msg)
	if red == msg {
		return err
	}
	return &redactedError{msg: red, err: err}
}

func (c Credentials) redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return c.redact(t)
	case []any:
		for i := range t {
			t[i] = c.redactValue(t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[c.redact(k)] = c.redactValue(val)
		}
		return out
	default:
		return v
	}
}

func (c Credentials) redactParams(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		b, _ := json.Marshal(c.redact(string(raw)))
		return b
	}
	b, err := json.Marshal(c.redactValue(v))
	if err != nil {
		return nil
	}
	return b
}
