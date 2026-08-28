package experiment

import (
	"fmt"
	"regexp"
)

var experimentIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,126}[A-Za-z0-9])?$`)

func ValidateExperimentID(id string) error {
	if !experimentIDPattern.MatchString(id) {
		return fmt.Errorf("invalid experiment id %q: use 1-128 letters, digits, or hyphens", id)
	}
	return nil
}
