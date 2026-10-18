package cli

import "context"

func Discover(ctx context.Context, d *Deps, domain string, fresh bool) (string, error) {
	return "", &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: "auto_not_implemented", Message: "--account auto is not implemented yet", Domain: domain}}
}
