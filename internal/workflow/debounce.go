package workflow

import (
	"fmt"
	"time"
)

// DebounceKeyword is the name of the start option of beehave run that holds a start back for a window.
const DebounceKeyword = "debounce"

// DebouncePeriod reads the debounce option of a start. An empty text is no debounce, and a window must be above zero.
func DebouncePeriod(text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}

	d, err := Duration(DebounceKeyword, text)
	if err != nil {
		return 0, err
	}

	if d <= 0 {
		return 0, debounceRange(text)
	}

	return d, nil
}

// CheckDebounce refuses a window below zero. Zero means no debounce.
func CheckDebounce(window time.Duration) error {
	if window < 0 {
		return debounceRange(window.String())
	}

	return nil
}

func debounceRange(text string) error {
	return fmt.Errorf("%s is %q, which must be above zero. Write a duration such as \"30s\", or leave %s out to start at once", DebounceKeyword, text, DebounceKeyword)
}
