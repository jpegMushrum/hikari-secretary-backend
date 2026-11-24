//go:build integration

package santa

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
	"os"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	*Service
	id  int64
	ids []int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	schema := fmt.Sprintf("santa_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if _, err = conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, conn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	f := &fixture{t: t, ctx: ctx, Service: New(db, []int64{999})}
	for i := 0; i < 4; i++ {
		var id int64
		err = db.QueryRow(ctx, "INSERT INTO people DEFAULT VALUES RETURNING id").Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(ctx, "INSERT INTO telegram_identities(person_id,telegram_user_id,display_name,last_seen_at) VALUES ($1,$2,$3,now())", id, 100+id, fmt.Sprintf("Participant %d", i))
		if err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, id)
	}
	v, err := f.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.id = v.ID
	again, err := f.Start(ctx)
	if err != nil || again.ID != f.id {
		t.Fatalf("repeat activation: %+v, %v", again, err)
	}
	if v, err := f.VisibleSeason(ctx, false); err != nil || v != nil {
		t.Fatalf("preparation is visible: %+v, %v", v, err)
	}
	deadline := time.Now().Add(time.Hour)
	exchange := deadline.Add(time.Hour)
	f.admin(AdminCommand{Action: "settings", Budget: "1000 ₽", RegistrationUntil: &deadline, ExchangeAt: &exchange, ReminderMinutes: 5})
	f.admin(AdminCommand{Action: "open"})
	return f
}
func (f *fixture) view() AdminView {
	f.t.Helper()
	v, e := f.Admin(f.ctx)
	if e != nil {
		f.t.Fatal(e)
	}
	return v
}
func (f *fixture) admin(c AdminCommand) {
	f.t.Helper()
	c.SeasonID = f.id
	c.Revision = f.view().Season.Revision
	if err := f.AdminAction(f.ctx, c); err != nil {
		f.t.Fatalf("%s: %v", c.Action, err)
	}
}
func (f *fixture) member(id int64, c MemberCommand) {
	f.t.Helper()
	c.SeasonID = f.id
	if err := f.MemberAction(f.ctx, id, c); err != nil {
		f.t.Fatalf("%s: %v", c.Action, err)
	}
}
func (f *fixture) join(id int64) { f.member(id, MemberCommand{Action: "join", Wishes: "Tea"}) }
func (f *fixture) seedLegacyPause() {
	f.t.Helper()
	if _, err := f.db.Exec(f.ctx, "UPDATE santa_seasons SET status='paused' WHERE id=$1", f.id); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) notifications(kind string, tg int64) int {
	f.t.Helper()
	var n int
	err := f.db.QueryRow(f.ctx, "SELECT count(*) FROM outbox WHERE topic='santa.notice' AND split_part(idempotency_key,':',4)=$1 AND ($2::bigint=0 OR (payload->>'telegram_user_id')::bigint=$2)", kind, tg).Scan(&n)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestDraftRevisionPrivacyAndConfirmation(t *testing.T) {
	f := setup(t)
	a, b, c, d := f.ids[0], f.ids[1], f.ids[2], f.ids[3]
	f.join(a)
	f.join(b)
	err := f.AdminAction(f.ctx, AdminCommand{SeasonID: f.id, Revision: f.view().Season.Revision, Action: "shuffle"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("draw with two people: %v", err)
	}
	f.join(c)
	f.admin(AdminCommand{Action: "shuffle"})
	v := f.view()
	if validatePairs(f.ids[:3], v.Pairs) != nil || f.notifications("draw", 0) != 0 {
		t.Fatal("invalid or published draft")
	}
	member, err := f.Member(f.ctx, a)
	if err != nil || member.Recipient != nil {
		t.Fatalf("private draft leaked: %+v %v", member, err)
	}
	stale := AdminCommand{SeasonID: f.id, Revision: v.Season.Revision, Action: "confirm"}
	f.join(d)
	if err = f.AdminAction(f.ctx, stale); !errors.Is(err, ErrConflict) || len(f.view().Pairs) != 0 {
		t.Fatalf("obsolete draft accepted: %v", err)
	}
	f.admin(AdminCommand{Action: "shuffle"})
	if err = f.AdminAction(f.ctx, AdminCommand{SeasonID: f.id, Revision: f.view().Season.Revision, Action: "draft", Pairs: []Pair{{a, b}, {b, b}, {c, d}, {d, c}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad draft accepted: %v", err)
	}
	intended := []Pair{{a, b}, {b, c}, {c, d}, {d, a}}
	f.admin(AdminCommand{Action: "draft", Pairs: intended})
	publish := AdminCommand{Action: "confirm", SeasonID: f.id, Revision: f.view().Season.Revision}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- f.AdminAction(f.ctx, publish) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if f.notifications("draw", 0) != 4 {
		t.Fatal("double confirmation must notify each participant exactly once")
	}
	for _, p := range intended {
		m, err := f.Member(f.ctx, p.GiverID)
		if err != nil || m.Recipient == nil || m.Recipient.PersonID != p.RecipientID {
			t.Fatalf("wrong recipient for %d: %+v %v", p.GiverID, m, err)
		}
	}
	f.member(b, MemberCommand{Action: "wishes", Wishes: "Coffee"})
	if f.notifications("wishes", a+100) != 1 || f.notifications("wishes", 0) != 1 {
		t.Fatal("wish change should only notify giver")
	}
	f.seedLegacyPause()
	_, err = f.Member(f.ctx, a)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause member access: %v", err)
	}
	err = f.MemberAction(f.ctx, a, MemberCommand{Action: "attendance", SeasonID: f.id, Attendance: "no"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("paused writes allowed")
	}
	f.admin(AdminCommand{Action: "open"})
	if err := f.Close(f.ctx, f.id); err != nil {
		t.Fatal(err)
	}
	next, err := f.Start(f.ctx)
	if err != nil || next.ID == f.id {
		t.Fatal("expected independent next season")
	}
	if err := f.Close(f.ctx, f.id); err != nil || f.view().Season.ID != next.ID {
		t.Fatal("replaying close for an archived season affected the current season", err)
	}
	var deliverable int
	if err := f.db.QueryRow(f.ctx, "SELECT count(*) FROM outbox WHERE aggregate_type='santa_season' AND aggregate_id=$1 AND ("+NoticeDispatchableSQL+")", f.id).Scan(&deliverable); err != nil || deliverable != 0 {
		t.Fatalf("archived notices still eligible: %d, %v", deliverable, err)
	}
}

func TestCancellationPreservesPrivateWishesOutsideRoster(t *testing.T) {
	f := setup(t)
	a, stranger := f.ids[0], f.ids[3]
	for _, id := range f.ids[:3] {
		f.join(id)
	}
	f.member(a, MemberCommand{Action: "wishes", Wishes: "Jasmine tea"})
	f.admin(AdminCommand{Action: "shuffle"})
	f.member(a, MemberCommand{Action: "cancel"})
	for _, id := range []int64{a, stranger} {
		v, err := f.Member(f.ctx, id)
		if err != nil || v.Participation != nil || v.Recipient != nil {
			t.Fatalf("expected an unregistered member, got %+v, %v", v, err)
		}
		if (id == a && v.DraftWishes != "Jasmine tea") || (id == stranger && v.DraftWishes != "") {
			t.Fatalf("unexpected private draft for %d: %q", id, v.DraftWishes)
		}
	}
	admin := f.view()
	if len(admin.Participants) != 2 || len(admin.Pairs) != 0 {
		t.Fatalf("cancelled participant or obsolete draft still present: %+v", admin)
	}
	err := f.AdminAction(f.ctx, AdminCommand{Action: "shuffle", SeasonID: f.id, Revision: admin.Season.Revision})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("private draft counted as participant: %v", err)
	}
	f.member(a, MemberCommand{Action: "join", Wishes: "Books"})
	v, err := f.Member(f.ctx, a)
	if err != nil || v.DraftWishes != "" || v.Participation == nil || v.Participation.Wishes != "Books" {
		t.Fatalf("rejoined with stale wishes: %+v, %v", v, err)
	}
	f.member(a, MemberCommand{Action: "cancel"})
	v, err = f.Member(f.ctx, a)
	if err != nil || v.DraftWishes != "Books" {
		t.Fatalf("second cancellation lost latest wishes: %+v, %v", v, err)
	}
}

func TestAdminNoticeLinksToParticipantProfile(t *testing.T) {
	f := setup(t)
	for _, id := range f.ids[:3] {
		f.join(id)
	}
	f.admin(AdminCommand{Action: "shuffle"})
	f.admin(AdminCommand{Action: "confirm"})
	a, b, c := f.ids[0], f.ids[1], f.ids[2]
	if _, err := f.db.Exec(f.ctx, `UPDATE telegram_identities SET username=CASE WHEN person_id=$1 THEN 'club_member' ELSE 'bad/name' END WHERE person_id IN ($1,$2)`, a, c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		personID int64
		command  MemberCommand
		wantURL  string
	}{
		{a, MemberCommand{Action: "attendance", Attendance: "no"}, "https://t.me/club_member"},
		{b, MemberCommand{Action: "request_exit"}, fmt.Sprintf("tg://user?id=%d", 100+b)},
		{c, MemberCommand{Action: "request_exit"}, fmt.Sprintf("tg://user?id=%d", 100+c)},
	} {
		f.member(tc.personID, tc.command)
		var got string
		if err := f.db.QueryRow(f.ctx, `SELECT payload->>'profile_url' FROM outbox WHERE topic='santa.notice' AND payload->>'telegram_user_id'='999' ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil || got != tc.wantURL {
			t.Fatalf("unexpected participant profile: got %q, want %q, err %v", got, tc.wantURL, err)
		}
	}
	var leaked bool
	if err := f.db.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM outbox WHERE topic='santa.notice' AND payload->>'path'='/santa' AND payload ? 'profile_url')`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("member notices contain profile link: %v, %v", leaked, err)
	}
}

func TestAttendanceReversibleExitAndTwoPersonRepair(t *testing.T) {
	f := setup(t)
	a, b, c := f.ids[0], f.ids[1], f.ids[2]
	for _, id := range f.ids[:3] {
		f.join(id)
	}
	f.admin(AdminCommand{Action: "shuffle"})
	err := f.MemberAction(f.ctx, a, MemberCommand{SeasonID: f.id, Action: "attendance", Attendance: "no"})
	if !errors.Is(err, ErrConflict) || f.notifications("attendance", 999) != 0 {
		t.Fatalf("attendance accepted before pairs were confirmed: %v", err)
	}
	f.admin(AdminCommand{Action: "draft", Pairs: []Pair{{a, b}, {b, c}, {c, a}}})
	description := "чайник"
	giftNote := MemberCommand{SeasonID: f.id, Action: "gift_description", RecipientID: b, GiftDescription: &description}
	if err := f.MemberAction(f.ctx, a, giftNote); !errors.Is(err, ErrConflict) {
		t.Fatalf("gift description before confirmation: %v", err)
	}
	f.admin(AdminCommand{Action: "confirm"})
	f.member(a, MemberCommand{Action: "attendance", Attendance: "no"})
	f.member(a, MemberCommand{Action: "attendance", Attendance: "yes"})
	if f.notifications("attendance", 999) != 2 {
		t.Fatal("attendance changes after confirmation must reach admin")
	}
	f.member(a, MemberCommand{Action: "gift", Gift: "ready", RecipientID: b})
	f.member(a, giftNote)
	f.member(c, MemberCommand{Action: "gift", Gift: "ready", RecipientID: a})
	f.member(b, MemberCommand{Action: "request_exit"})
	f.member(b, MemberCommand{Action: "retract_exit"})
	if f.notifications("exit", 999) != 2 || len(f.view().Pairs) != 3 {
		t.Fatal("request/retract unexpectedly changed assignments")
	}
	f.admin(AdminCommand{Action: "exclude", PersonID: b, Pairs: []Pair{{a, c}, {c, a}}})
	for _, who := range []int64{a, b, c} {
		m, err := f.Member(f.ctx, who)
		if err != nil {
			t.Fatal(err)
		}
		switch who {
		case a:
			if m.Recipient.PersonID != c || m.Participation.Gift != "unknown" {
				t.Fatal("changed assignment retained gift status")
			}
			if m.Participation.GiftDescription != description {
				t.Fatal("changing recipient discarded the prepared gift description")
			}
		case b:
			if m.Participation.Status != "excluded" || m.Recipient != nil {
				t.Fatal("excluded member still participates")
			}
		case c:
			if m.Recipient.PersonID != a || m.Participation.Gift != "ready" {
				t.Fatal("unaffected gift changed")
			}
		}
	}
	if f.notifications("replacement", 0) != 1 || f.notifications("replacement", 100+a) != 1 {
		t.Fatal("unrelated members were notified")
	}
	err = f.MemberAction(f.ctx, b, MemberCommand{Action: "join", SeasonID: f.id, Wishes: "again"})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("excluded member rejoined")
	}
	err = f.MemberAction(f.ctx, a, MemberCommand{Action: "gift", SeasonID: f.id, RecipientID: b, Gift: "ready"})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("stale card updated gift status")
	}
	if err := f.MemberAction(f.ctx, a, giftNote); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted description for stale recipient: %v", err)
	}
	giftNote.RecipientID = c
	description = strings.Repeat("光", 2001)
	if err := f.MemberAction(f.ctx, a, giftNote); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted oversized gift description: %v", err)
	}
	description = ""
	f.member(a, giftNote)
	updated, err := f.Member(f.ctx, a)
	if err != nil || updated.Participation.GiftDescription != "" {
		t.Fatalf("failed to clear gift description: %v", err)
	}
}

func TestReminderUsesEditedDateAndDoesNotRepeat(t *testing.T) {
	f := setup(t)
	for _, id := range f.ids[:3] {
		f.join(id)
	}
	f.admin(AdminCommand{Action: "shuffle"})
	f.admin(AdminCommand{Action: "confirm"})
	if err := f.EnqueueReminders(f.ctx); err != nil || f.notifications("reminder", 0) != 0 {
		t.Fatalf("early reminders: %v", err)
	}
	deadline := time.Now().Add(-time.Hour)
	exchange := time.Now().Add(2 * time.Minute)
	f.admin(AdminCommand{Action: "settings", Budget: "1000", RegistrationUntil: &deadline, ExchangeAt: &exchange, ReminderMinutes: 5})
	f.seedLegacyPause()
	if err := f.EnqueueReminders(f.ctx); err != nil || f.notifications("reminder", 0) != 0 {
		t.Fatalf("paused reminders: %v", err)
	}
	f.admin(AdminCommand{Action: "open"})
	first := f.ids[0]
	member, err := f.Member(f.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	f.member(first, MemberCommand{Action: "attendance", Attendance: "no"})
	f.member(first, MemberCommand{Action: "gift", RecipientID: member.Recipient.PersonID, Gift: "not_ready"})
	if f.notifications("gift", 0) != 0 {
		t.Fatal("a gift that is not ready does not imply a missed deadline")
	}
	f.member(first, MemberCommand{Action: "gift", RecipientID: member.Recipient.PersonID, Gift: "ordered"})
	for range 2 {
		if err := f.EnqueueReminders(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.notifications("reminder", 0) != 3 {
		t.Fatal("expected exactly one prompt per person")
	}
	var body string
	if err := f.db.QueryRow(f.ctx, "SELECT payload->>'text' FROM outbox WHERE payload->>'kind'='reminder' AND (payload->>'telegram_user_id')::bigint=$1", 100+first).Scan(&body); err != nil || !strings.Contains(body, "не смогу прийти") || !strings.Contains(body, "заказан (в пути)") {
		t.Fatalf("missing saved responses: %v, %s", err, body)
	}

	// Exercise the same eligibility predicate used by delivery claims and the
	// local preview. A retry can remain queued while dates or membership change.
	claim := func() (int64, bool) {
		t.Helper()
		var id int64
		err := f.db.QueryRow(f.ctx, `UPDATE outbox SET status='processing' WHERE id=(
			SELECT id FROM outbox WHERE payload->>'kind'='reminder' AND status IN ('pending','failed')
			AND available_at<=now() AND (`+NoticeDispatchableSQL+`) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1
		) RETURNING id`).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return id, true
	}
	postpone := time.Now().Add(24 * time.Hour)
	f.admin(AdminCommand{Action: "settings", Budget: "1000", RegistrationUntil: &deadline, ExchangeAt: &postpone, ReminderMinutes: 5})
	if _, ok := claim(); ok {
		t.Fatal("claimed a reminder scheduled before postponed exchange")
	}
	var early int
	if err := f.db.QueryRow(f.ctx, "SELECT count(*) FROM outbox WHERE payload->>'kind'='reminder' AND available_at<now()+interval '23 hours'").Scan(&early); err != nil || early != 0 {
		t.Fatalf("did not reschedule pending outbox rows: %d %v", early, err)
	}
	f.admin(AdminCommand{Action: "settings", Budget: "1000", RegistrationUntil: &deadline, ExchangeAt: &exchange, ReminderMinutes: 5})
	f.seedLegacyPause()
	if _, ok := claim(); ok {
		t.Fatal("paused season claimed reminder")
	}
	f.admin(AdminCommand{Action: "open"})
	a, b, c := f.ids[0], f.ids[1], f.ids[2]
	f.admin(AdminCommand{Action: "exclude", PersonID: a, Pairs: []Pair{{b, c}, {c, b}}})
	for range 2 {
		if _, ok := claim(); !ok {
			t.Fatal("remaining participant reminder was not claimable")
		}
	}
	if _, ok := claim(); ok {
		t.Fatal("excluded participant reminder was claimable")
	}
	_, err = f.db.Exec(f.ctx, "UPDATE outbox SET status='failed' WHERE status='processing'")
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	f.admin(AdminCommand{Action: "settings", Budget: "1000", RegistrationUntil: &deadline, ExchangeAt: &past, ReminderMinutes: 5})
	if _, ok := claim(); ok {
		t.Fatal("claimed a failed reminder after the exchange")
	}
	f.admin(AdminCommand{Action: "settings", Budget: "1000", RegistrationUntil: &deadline, ExchangeAt: &exchange, ReminderMinutes: 5})
	if err := f.Close(f.ctx, f.id); err != nil {
		t.Fatal(err)
	}
	if _, ok := claim(); ok {
		t.Fatal("closed season claimed reminder")
	}
}
