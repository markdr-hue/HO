/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package security

import (
	"fmt"
	"regexp"
)

// validIdentifier matches SQL identifiers: must start with a letter,
// contain only alphanumeric and underscore characters.
var validIdentifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// ValidateColumnName checks that a column name is safe for SQL interpolation.
// Returns an error if the name contains characters outside [a-zA-Z0-9_] or
// does not start with a letter.
func ValidateColumnName(name string) error {
	if !validIdentifier.MatchString(name) {
		return fmt.Errorf("invalid column name: %q (must start with letter, contain only alphanumeric/underscore)", name)
	}
	return nil
}

// ValidateColumnNames validates a slice of column names for SQL safety.
func ValidateColumnNames(names []string) error {
	for _, name := range names {
		if err := ValidateColumnName(name); err != nil {
			return err
		}
	}
	return nil
}
