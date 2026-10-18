package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const SchemaVersion = 1

const (
	StatusSuccess              = "success"
	StatusFailed               = "failed"
	StatusPartial              = "partial"
	StatusUnknown              = "unknown"
	StatusConfirmationRequired = "confirmation_required"
)

const (
	ExitOK         = 0
	ExitFailure    = 1
	ExitUsage      = 2
	ExitIncomplete = 3
	ExitStale      = 4
)

type ErrorItem struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	ProviderCode string `json:"provider_code,omitempty"`
	Account      string `json:"account,omitempty"`
	Domain       string `json:"domain,omitempty"`
	Operation    string `json:"operation,omitempty"`
	Details      any    `json:"details,omitempty"`
}

type Envelope struct {
	SchemaVersion int         `json:"schema_version"`
	Status        string      `json:"status"`
	Data          any         `json:"data"`
	Errors        []ErrorItem `json:"errors"`
	Warnings      []string    `json:"warnings"`
}

type CodedError struct {
	Exit int
	Item ErrorItem
}

func (e *CodedError) Error() string { return e.Item.Message }

type Env struct {
	Stdout    io.Writer
	Stderr    io.Writer
	Stdin     io.Reader
	LookupEnv func(string) (string, bool)
	IsTTY     func() bool
	Now       func() time.Time
	Home      string
}

type Output struct {
	Format string
	Env    Env
}

func (o *Output) writeJSON(e Envelope) {
	if e.Errors == nil {
		e.Errors = []ErrorItem{}
	}
	if e.Warnings == nil {
		e.Warnings = []string{}
	}
	e.SchemaVersion = SchemaVersion
	enc := json.NewEncoder(o.Env.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(e)
}

func (o *Output) Done(status string, data any, warnings []string, text func(io.Writer)) int {
	if o.Format == "json" {
		o.writeJSON(Envelope{Status: status, Data: data, Warnings: warnings})
	} else {
		if text != nil {
			text(o.Env.Stdout)
		}
		for _, w := range warnings {
			fmt.Fprintf(o.Env.Stderr, "warning: %s\n", w)
		}
	}
	switch status {
	case StatusSuccess:
		return ExitOK
	case StatusPartial, StatusUnknown:
		return ExitIncomplete
	}
	return ExitFailure
}

func (o *Output) Fail(err error, partial any) int {
	var ce *CodedError
	if !errors.As(err, &ce) {
		ce = &CodedError{Exit: ExitFailure, Item: ErrorItem{Code: "error", Message: err.Error()}}
	}
	status := StatusFailed
	if ce.Exit == ExitIncomplete {
		status = StatusPartial
	}
	if o.Format == "json" {
		o.writeJSON(Envelope{Status: status, Data: partial, Errors: []ErrorItem{ce.Item}})
	} else {
		fmt.Fprintf(o.Env.Stderr, "error: %s\n", ce.Item.Message)
	}
	return ce.Exit
}
