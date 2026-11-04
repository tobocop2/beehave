package storetest_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/storetest"
)

// memWriter is a correct in-memory writer and its probe. Flaws switch on one wrong behavior each.
type memWriter struct {
	keys     map[string]string
	statuses map[string]store.Status
	notes    map[string]map[string]bool
	count    int

	down, dupCreate, dupComment, ignoreStatus bool
}

func newMemWriter() *memWriter {
	return &memWriter{keys: map[string]string{}, statuses: map[string]store.Status{}, notes: map[string]map[string]bool{}}
}

func (m *memWriter) Create(_ context.Context, key string, _ store.NewItem) (string, bool, error) {
	if m.down {
		return "", false, errDown
	}

	if id, found := m.keys[key]; found && !m.dupCreate {
		return id, false, nil
	}

	m.count++
	id := "i" + strconv.Itoa(m.count)
	m.keys[key] = id
	m.statuses[id] = store.StatusOpen

	return id, true, nil
}

func (m *memWriter) SetStatus(_ context.Context, id string, status store.Status) error {
	if m.down {
		return errDown
	}

	if !m.ignoreStatus {
		m.statuses[id] = status
	}

	return nil
}

func (m *memWriter) Assign(context.Context, string, string) error { return errIf(m.down) }

func errIf(down bool) error {
	if down {
		return errDown
	}

	return nil
}

func (m *memWriter) Comment(_ context.Context, id, key, _ string) error {
	if m.down {
		return errDown
	}

	if m.notes[id] == nil {
		m.notes[id] = map[string]bool{}
	}

	m.notes[id][key+strconv.Itoa(len(m.notes[id])*boolInt(m.dupComment))] = true

	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}

	return 0
}

func (m *memWriter) Items() int                    { return m.count }
func (m *memWriter) Status(id string) store.Status { return m.statuses[id] }
func (m *memWriter) Comments(id string) int        { return len(m.notes[id]) }

func TestCheckWriter_AcceptsACorrectWriter(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	mem := newMemWriter()
	storetest.CheckWriter(rec, mem, mem)
	assert.Empty(t, rec.failures)
}

func TestCheckWriter_CatchesEachFlaw(t *testing.T) {
	t.Parallel()

	for name, flaw := range map[string]func(*memWriter){
		"a repeated key files a second item":  func(m *memWriter) { m.dupCreate = true },
		"a repeated comment key adds another": func(m *memWriter) { m.dupComment = true },
		"a status that is not stored":         func(m *memWriter) { m.ignoreStatus = true },
		"a writer that fails":                 func(m *memWriter) { m.down = true },
	} {
		rec := &recorder{}
		mem := newMemWriter()
		flaw(mem)
		storetest.CheckWriter(rec, mem, mem)
		assert.NotEmpty(t, rec.failures, name)
	}
}

func TestCheckWriterFails_AcceptsAWriterThatReturnsErrors(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	mem := newMemWriter()
	mem.down = true
	storetest.CheckWriterFails(rec, mem)
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	storetest.CheckWriterFails(rec, newMemWriter())
	assert.Len(t, rec.failures, 4)
}
