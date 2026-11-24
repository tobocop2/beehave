package skypilot

import (
	"fmt"
	"strconv"
	"strings"
)

// sizeRow is one row of the size table: the instance type for a queue of up to upTo jobs. The last row has upTo zero and takes every longer queue.
type sizeRow struct {
	upTo         int
	instanceType string
}

// sizeTable is the instance types of the cluster by queue length, in the order of the project file. An empty table leaves the choice to SkyPilot.
type sizeTable []sizeRow

// parseSizes builds the size table from the two keys. `instance_type` is a table of one row, and a table of two keys is refused.
func parseSizes(values block) (sizeTable, error) {
	switch {
	case values.instanceType != "" && values.instanceSizes != "":
		return nil, fmt.Errorf("%w: `host.skypilot.instance_type` and `host.skypilot.instance_sizes` are both set. Keep one: `instance_sizes` with one row replaces `instance_type`", ErrConfig)
	case values.instanceType != "":
		return sizeTable{{instanceType: values.instanceType}}, nil
	case values.instanceSizes == "":
		return nil, nil
	}

	return parseTable(values.instanceSizes)
}

// parseTable reads the rows of the text and checks that the limits rise.
func parseTable(text string) (sizeTable, error) {
	parts := strings.Split(text, ",")
	table := make(sizeTable, len(parts))

	for i, part := range parts {
		row, err := parseRow(strings.TrimSpace(part), i == len(parts)-1)
		if err != nil {
			return nil, err
		}

		if i > 0 && row.upTo != 0 && row.upTo <= table[i-1].upTo {
			return nil, fmt.Errorf("%w: `host.skypilot.instance_sizes` row %q must give a limit above the limit before it (%d)", ErrConfig, part, table[i-1].upTo)
		}

		table[i] = row
	}

	return table, nil
}

// parseRow reads `<limit>:<type>`, or a bare `<type>` for the last row.
func parseRow(text string, last bool) (sizeRow, error) {
	limit, instanceType, bounded := strings.Cut(text, ":")
	if !bounded {
		instanceType = text
	}

	switch {
	case !wordPattern.MatchString(instanceType):
		return sizeRow{}, fmt.Errorf("%w: `host.skypilot.instance_sizes` type %q must be letters, digits, dots, dashes and underscores", ErrConfig, instanceType)
	case last && bounded:
		return sizeRow{}, fmt.Errorf("%w: the last row of `host.skypilot.instance_sizes` (%q) must name a type with no limit, so every queue length has a size", ErrConfig, text)
	case !last && !bounded:
		return sizeRow{}, fmt.Errorf("%w: row %q of `host.skypilot.instance_sizes` needs a limit, as `3:n2-standard-4`", ErrConfig, text)
	case !bounded:
		return sizeRow{instanceType: instanceType}, nil
	}

	return parseLimit(limit, instanceType)
}

// parseLimit reads the limit of a row: a whole number above zero.
func parseLimit(limit, instanceType string) (sizeRow, error) {
	upTo, err := strconv.Atoi(limit)
	if err != nil || upTo < 1 {
		return sizeRow{}, fmt.Errorf("%w: the limit %q in `host.skypilot.instance_sizes` must be a whole number above zero", ErrConfig, limit)
	}

	return sizeRow{upTo: upTo, instanceType: instanceType}, nil
}

// typeFor is the instance type for a queue of queued jobs: the first row whose limit holds the queue. An empty table gives no type.
func (t sizeTable) typeFor(queued int) string {
	for _, row := range t {
		if row.upTo == 0 || queued <= row.upTo {
			return row.instanceType
		}
	}

	return ""
}
