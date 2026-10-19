package cli

import (
	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruzone"
)

func cliRules() plan.Rules {
	return plan.Rules{Caps: regruzone.Capabilities(), Protected: regruzone.IsProtected, ValidateWrite: regruzone.ValidateWrite}
}
