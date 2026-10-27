package brief

import "strings"

const noReason = "none:"

// Slot is a value for the `[NAME]` marker of a brief.
type Slot struct {
	Name  string
	Value string
}

// ReadSlots returns the `NAME=value` lines of a slots file, in order. A blank path names no slots.
// A line with no name before the `=` is not a slot.
func ReadSlots(path string) ([]Slot, error) {
	if path == "" {
		return nil, nil
	}

	if !isFile(path) {
		return nil, refuse("no slots file at %s. Check the path of the slots file.", path)
	}

	text, err := ReadText(path)
	if err != nil {
		return nil, err
	}

	return parseSlots(text), nil
}

func parseSlots(text string) []Slot {
	var slots []Slot

	for _, line := range awkLines(text) {
		if at := strings.Index(line, "="); at > 0 {
			slots = append(slots, Slot{Name: line[:at], Value: line[at+1:]})
		}
	}

	return slots
}

// Fill replaces each `[NAME]` of the slots in the text, one slot after the other.
func Fill(text string, slots []Slot) string {
	for _, slot := range slots {
		text = strings.ReplaceAll(text, "["+slot.Name+"]", slot.Value)
	}

	return text
}

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
