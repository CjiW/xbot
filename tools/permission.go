package tools

import (
	"fmt"
	"strings"
)

// validateRunAsReason ensures that run_as and reason are either both set or both empty.
func validateRunAsReason(runAs, reason string) error {
	if (strings.TrimSpace(runAs) == "") != (strings.TrimSpace(reason) == "") {
		return fmt.Errorf("run_as and reason must be provided together")
	}
	return nil
}
