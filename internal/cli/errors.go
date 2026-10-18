package cli

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lavr/regru-cli/internal/config"
	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/regruzone"
	"github.com/lavr/regru-cli/internal/zone"
)

func coded(exit int, code string, err error) CodedError {
	return CodedError{Exit: exit, Item: ErrorItem{Code: code, Message: err.Error()}}
}

func ToCoded(err error) CodedError {
	if err == nil {
		return CodedError{Exit: ExitOK}
	}
	var (
		ce  *CodedError
		cfg *config.Error
		ne  *zone.NameError
		ve  *zone.ValueError
		me  *plan.ManifestError
		pe  *plan.PolicyError
		ze  *regruzone.Error
		ue  *regruapi.UnknownOutcomeError
		de  *regruapi.DomainError
		ae  *regruapi.APIError
		ie  *regruapi.IncompleteReadError
		te  *regruapi.TransportError
	)
	switch {
	case errors.As(err, &ce):
		return *ce
	case errors.As(err, &cfg):
		return coded(ExitUsage, cfg.Code, err)
	case errors.As(err, &ne):
		return coded(ExitUsage, ne.Code(), err)
	case errors.As(err, &ve):
		return coded(ExitUsage, ve.Code(), err)
	case errors.As(err, &me):
		return coded(ExitUsage, me.Code(), err)
	case errors.As(err, &pe):
		return coded(ExitFailure, pe.Code, err)
	case errors.As(err, &ze):
		if ze.Code() == "unsupported_type" || ze.Code() == "unsupported_value" {
			return coded(ExitUsage, ze.Code(), err)
		}
		return coded(ExitFailure, ze.Code(), err)
	case errors.As(err, &ue):
		return coded(ExitIncomplete, "unknown_outcome", err)
	case errors.As(err, &de):
		c := coded(ExitFailure, "domain_error", err)
		c.Item.ProviderCode = de.Code
		c.Item.Domain = de.Domain
		if len(de.Params) > 0 {
			c.Item.Details = json.RawMessage(de.Params)
		}
		return c
	case errors.As(err, &ae):
		c := coded(ExitFailure, "api_error", err)
		c.Item.ProviderCode = ae.Code
		return c
	case errors.As(err, &ie):
		return coded(ExitFailure, "incomplete_read", err)
	case errors.As(err, &te):
		return coded(ExitFailure, "transport_error", err)
	case errors.Is(err, context.DeadlineExceeded):
		return coded(ExitFailure, "timeout", err)
	case errors.Is(err, context.Canceled):
		return coded(ExitFailure, "interrupted", err)
	}
	return coded(ExitFailure, "error", err)
}
