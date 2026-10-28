package brief

import (
	"os"
	"strings"
)

const (
	noReason      = "none:"
	sectionPrefix = "## "
)

// CheckSlot refuses a lane field that holds no path and no `none: <reason>`. The field is named as the caller spells it.
func CheckSlot(field, value, lane string) error {
	switch {
	case value == "":
		return refuse("the lane %s has no %s; give a path with the %s field, or 'none: <reason>' for a change with no output. "+
			"An impl brief names the acceptance script (acceptance) and its recorded result at the base (base_result).",
			lane, field, field)
	case strings.HasPrefix(value, noReason):
		if strings.Trim(strings.TrimPrefix(value, noReason), blanks) == "" {
			return refuse("the %s field says 'none:' with no reason. Write 'none: <reason>'.", field)
		}

		return nil
	case value == "none" || strings.HasPrefix(value, "[") || strings.HasPrefix(value, "<"):
		return refuse("the %s field holds '%s', which is a placeholder. Give a path, or 'none: <reason>'.", field, value)
	}

	return nil
}

func hasLine(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
