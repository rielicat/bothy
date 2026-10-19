package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// Another person's schedule keeps its name and timing out of the log, as
// their schedules list does; the reader's own schedules keep theirs.
func TestAuditKeepsOtherPeoplesScheduleNamesPrivate(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	row := func(owner core.UserID, id, name string) {
		d, _ := json.Marshal(map[string]any{"schedule_id": id, "principal_id": core.UserPrincipal(owner), "name": name, "kind": "cron", "spec": "0 6 * * 1", "tz": "UTC"})
		e := core.AuditEntry{At: time.Now(), Actor: "user:" + string(owner), Action: "schedule.create", Target: "schedule:" + id, Outcome: "ok", Detail: d}
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	row(h.member.user, "sch_1", "plan-for-max")
	row(h.admin.user, "sch_2", "admins-own")
	rows := decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit?action=schedule.", nil, h.admin))
	got := map[string]dto.AuditRow{}
	for _, r := range rows.Items {
		got[r.Target] = r
	}
	theirs, mine := got["schedule:sch_1"], got["schedule:sch_2"]
	if theirs.Summary != "Created a schedule" || strings.Contains(string(theirs.Detail), "plan-for-max") || strings.Contains(string(theirs.Detail), "0 6") {
		t.Fatalf("another person's schedule row = %q %s", theirs.Summary, theirs.Detail)
	}
	if mine.Summary != "Created the schedule “admins-own”" {
		t.Fatalf("own schedule row = %q %s", mine.Summary, mine.Detail)
	}
	expect(t, h.do("GET", "/api/audit", nil, h.member), http.StatusForbidden)
}
