package memstore

import (
	"reflect"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Store(t, func(*testing.T) core.Store { return New() })
}

// TestCloneCopiesEveryContainer guards Tx rollback: a table added to state but
// forgotten in clone would leak a failed transaction's writes.
func TestCloneCopiesEveryContainer(t *testing.T) {
	st := newState()
	st.messages = make([]core.Message, 1)
	st.pins = make([]core.Pin, 1)
	st.llmCalls = make([]llmRow, 1)
	st.cardMsgs = make([]cardMsgRow, 1)
	st.audit = make([]auditRow, 1)
	st.egress = make([]core.EgressRecord, 1)
	st.memEvents = make([]core.MemoryEvent, 1)
	st.vaultKeys = make([]vaultKeyRow, 1)
	c := st.clone()
	orig, cl := reflect.ValueOf(st), reflect.ValueOf(c)
	for i := range orig.NumField() {
		name := orig.Type().Field(i).Name
		switch orig.Field(i).Kind() {
		case reflect.Map, reflect.Slice:
			if orig.Field(i).IsNil() {
				t.Errorf("%s is nil in newState", name)
			} else if orig.Field(i).Pointer() == cl.Field(i).Pointer() {
				t.Errorf("clone shares %s with the original", name)
			}
		}
	}
}
