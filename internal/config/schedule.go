package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// defaultTimezone is the zone of a schedule that names none.
const defaultTimezone = "UTC"

// scheduleName is the form of a schedule name: a letter, then letters, digits and hyphens, all in lower case.
var scheduleName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// cronParser reads cron text the way DBOS does. DBOS starts its scheduler with cron.New(cron.WithSeconds()), and that option sets this parser:
// a leading seconds field, so a 5-field text is refused, and the descriptors such as @weekly and @every.
func cronParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

// rawSchedule is a schedule block as written. At is the position of its call.
type rawSchedule struct {
	At       syntax.Position
	Name     string
	Cron     string
	Workflow string
	Item     string
	Timezone text
	CatchUp  bool
}

// scheduleSpec is the schema of the schedule constructor.
func scheduleSpec() spec {
	return spec{"schedule", []string{"name", "cron", "workflow", "item", "timezone?", "catch_up?"}}
}

// newSchedule is the constructor of the schedule block.
func newSchedule(at syntax.Position, kwargs []starlark.Tuple) (*rawSchedule, error) {
	raw := &rawSchedule{At: at}
	err := scheduleSpec().unpack(at, kwargs, &raw.Name, &raw.Cron, &raw.Workflow, &raw.Item, &raw.Timezone, &raw.CatchUp)

	return raw, err
}

// schedules is the schedules argument: a list of schedule blocks.
type schedules []*rawSchedule

// Unpack stores the blocks of a list or a tuple. It refuses any other value, and an item that is not a schedule block.
func (s *schedules) Unpack(v starlark.Value) error {
	seq, ok := v.(starlark.Sequence)
	if !ok || v.Type() == "dict" || v.Type() == "set" {
		return fmt.Errorf("got %s, want a list of %s(...)", v.Type(), scheduleSpec().name)
	}

	*s = nil

	return s.collect(seq)
}

// collect appends the schedule block of each item of seq.
func (s *schedules) collect(seq starlark.Sequence) error {
	iter := seq.Iterate()
	defer iter.Done()

	var item starlark.Value
	for iter.Next(&item) {
		held := nested[rawSchedule]{want: scheduleSpec().name}
		if err := held.Unpack(item); err != nil {
			return err
		}

		*s = append(*s, held.block)
	}

	return nil
}

// schedulesOf checks the schedules of the project file at path and returns them in file order.
// A relative workflow path is read from the folder of the project file. The workflow file is parsed by `check config`, because the workflow package depends on this one.
func schedulesOf(path string, raw rawProject) ([]model.Schedule, error) {
	var (
		out      []model.Schedule
		problems []error
		seen     = map[string]bool{}
	)

	for _, block := range raw.Schedules {
		schedule, err := scheduleOf(filepath.Dir(path), *block)
		if err = errors.Join(err, firstUse(seen, *block)); err != nil {
			problems = append(problems, err)

			continue
		}

		out = append(out, schedule)
	}

	return out, errors.Join(problems...)
}

// firstUse refuses a block whose name an earlier block already has, and records the name otherwise.
func firstUse(seen map[string]bool, block rawSchedule) error {
	if seen[block.Name] {
		return refuseAt(block.At, "`name` is %q, and another schedule has it. Give each schedule its own name.", block.Name)
	}

	seen[block.Name] = true

	return nil
}

// nameText refuses a name that is not in the form of a schedule name.
func nameText(block rawSchedule) error {
	if !scheduleName.MatchString(block.Name) {
		return refuseAt(block.At, "`name` is %q. Write a lower case letter, then lower case letters, digits and hyphens.", block.Name)
	}

	return nil
}

// itemText refuses an item that cannot be part of an item id.
func itemText(block rawSchedule) error {
	if err := requireText(block.At, "item", block.Item); err != nil {
		return err
	}

	if model.SafeName(block.Item) != block.Item {
		return refuseAt(block.At, "`item` is %q. The item id is built from it, so write letters, digits and hyphens only.", block.Item)
	}

	return nil
}

// scheduleOf checks one schedule block.
func scheduleOf(dir string, block rawSchedule) (model.Schedule, error) {
	zone, zoneErr := timezoneOf(block)

	// The cron text is read with the zone of the schedule, so a zone that is refused has its own message and the cron text waits for a good zone.
	var cronErr error
	if zoneErr == nil {
		cronErr = cronText(block, zone)
	}

	if err := errors.Join(nameText(block), cronErr, zoneErr, itemText(block)); err != nil {
		return model.Schedule{}, err
	}

	workflow, err := workflowPath(dir, block)
	if err != nil {
		return model.Schedule{}, err
	}

	return model.Schedule{Name: block.Name, Cron: block.Cron, Workflow: workflow, Item: block.Item, Timezone: zone, CatchUp: block.CatchUp}, nil
}

// cronText refuses cron text that the parser of DBOS refuses. DBOS parses the text after the prefix "CRON_TZ=<zone> ", so the same string is parsed here. The prefix changes no result today, because the zone is checked before; it stays so that the code matches DBOS.
// The text may not hold a zone prefix of its own, because the `timezone` key sets the zone.
func cronText(block rawSchedule, zone string) error {
	if err := requireText(block.At, "cron", block.Cron); err != nil {
		return err
	}

	if strings.HasPrefix(block.Cron, "TZ=") || strings.HasPrefix(block.Cron, "CRON_TZ=") {
		return refuseAt(block.At, "`cron` is %q and starts with a zone. Remove the zone from the text and set the `timezone` key.", block.Cron)
	}

	if _, err := cronParser().Parse("CRON_TZ=" + zone + " " + block.Cron); err != nil {
		return refuseAt(block.At, "`cron` is %q: %w. Write six fields with the seconds first, such as \"0 0 9 * * 1\", or a descriptor such as \"@weekly\".", block.Cron, err)
	}

	return nil
}

// timezoneOf returns the zone of the schedule, UTC when the call names none. A zone that the system cannot load is refused.
func timezoneOf(block rawSchedule) (string, error) {
	if !block.Timezone.Set {
		return defaultTimezone, nil
	}

	if err := requireText(block.At, "timezone", block.Timezone.Value); err != nil {
		return "", err
	}

	if _, err := time.LoadLocation(block.Timezone.Value); err != nil {
		return "", refuseAt(block.At, "`timezone` is %q: %w. Write the name of an IANA zone, such as \"Europe/Berlin\".", block.Timezone.Value, err)
	}

	return block.Timezone.Value, nil
}

// workflowPath returns the absolute path of the workflow file. The file must exist.
func workflowPath(dir string, block rawSchedule) (string, error) {
	if err := requireText(block.At, "workflow", block.Workflow); err != nil {
		return "", err
	}

	path, err := expandHome(block.Workflow)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}

	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", refuseAt(block.At, "`workflow` is %q, and that is not a file you can read. Write the path of a workflow file.", path)
	}

	return path, nil
}
