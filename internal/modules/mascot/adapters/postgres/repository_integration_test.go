//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
)

type fixture struct {
	*Repository
	ctx    context.Context
	season int64
	round  int
	people [3]int64
	t      *testing.T
}

func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	requireOK(t, err)
	t.Cleanup(func() { conn.Close(ctx) })
	schema := fmt.Sprintf("mascot_test_%d", time.Now().UnixNano())
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	requireOK(t, err)
	t.Cleanup(func() {
		_, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+schema)
	requireOK(t, err)
	requireOK(t, migrations.Run(ctx, conn, "../../../../../migrations"))
	cfg, err := pgxpool.ParseConfig(dsn)
	requireOK(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	requireOK(t, err)
	t.Cleanup(pool.Close)
	f := &fixture{Repository: NewRepository(pool), ctx: ctx, t: t}
	for i := range f.people {
		requireOK(t, pool.QueryRow(ctx, "INSERT INTO people(full_name) VALUES($1) RETURNING id", fmt.Sprintf("Person %d", i)).Scan(&f.people[i]))
	}
	_, err = pool.Exec(ctx, `WITH e AS(INSERT INTO events(title,starts_at,ends_at) VALUES('Meet',now()+interval '1 day',now()+interval '2 days') RETURNING id)
 INSERT INTO event_registrations(event_id,person_id,registered_at) SELECT e.id,$1,now() FROM e`, f.people[0])
	requireOK(t, err)
	season, err := f.Start(ctx)
	requireOK(t, err)
	f.season = season.ID
	f.ok(0, true, application.Command{Action: "open", Deadline: time.Now().AddDate(0, 0, 7).Format("2006-01-02")})
	return f
}

func (f *fixture) do(person int64, admin bool, c application.Command) error {
	c.SeasonID = f.season
	c.Round = f.round
	return f.Action(f.ctx, person, admin, c)
}

func (f *fixture) ok(person int64, admin bool, c application.Command) {
	f.t.Helper()
	requireOK(f.t, f.do(person, admin, c))
}

func (f *fixture) view(person int64, admin bool) application.View {
	f.t.Helper()
	v, err := f.View(f.ctx, person, admin)
	requireOK(f.t, err)
	return v
}

func (f *fixture) entry(name string) int64 {
	f.t.Helper()
	person := f.people[0]
	key := "photo-" + name
	requireOK(f.t, f.ReserveUpload(f.ctx, person, f.season, key))
	requireOK(f.t, f.FinishUpload(f.ctx, person, key))
	c := application.Command{Action: "submit", Name: name, Description: "Концепт", Images: []domain.Image{{Key: key, AI: true}}, RequestID: name}
	f.ok(person, false, c)
	f.ok(person, false, c)
	entries := f.view(person, false).Entries
	return entries[len(entries)-1].ID
}

func (f *fixture) checkRead(person int64, admin bool, key string, want bool) {
	f.t.Helper()
	ok, err := f.CanReadImage(f.ctx, person, admin, key)
	requireOK(f.t, err)
	if ok != want {
		f.t.Fatalf("image access got %t want %t", ok, want)
	}
}

func (f *fixture) moderate(ids ...int64) {
	requireOK(f.t, f.EndTestSubmissions(f.ctx, f.season))
	yes := true
	for _, id := range ids {
		f.ok(0, true, application.Command{Action: "moderate", Approved: &yes, EntryID: id})
	}
	f.ok(0, true, application.Command{Action: "collect"})
	f.round++
}

func TestElectionRunoffsAndPrivacy(t *testing.T) {
	f := setup(t)
	a, b, late := f.people[0], f.people[1], f.people[2]
	if err := f.ReserveUpload(f.ctx, b, f.season, "ineligible"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	first, second := f.entry("А"), f.entry("Б")
	f.checkRead(b, false, "photo-А", false)
	if len(f.view(b, false).Entries) != 0 {
		t.Fatal("unpublished applications exposed")
	}
	f.moderate(first, second)
	for _, person := range []int64{a, b, late} {
		f.ok(person, false, application.Command{Action: "ready"})
	}
	f.ok(0, true, application.Command{Action: "exclude", PersonID: late})
	if err := f.do(late, false, application.Command{Action: "ready"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	f.ok(0, true, application.Command{Action: "start"})
	f.ok(0, true, application.Command{Action: "start"})
	f.checkRead(late, false, "photo-А", false)
	f.checkRead(b, false, "photo-А", true)
	yes, no := true, false
	for _, id := range []int64{first, second} {
		v := f.view(a, false)
		if v.Card == nil || v.Card.Author != "" || v.Remaining == 0 {
			t.Fatalf("card: %+v", v)
		}
		f.ok(a, false, application.Command{Action: "vote", EntryID: id, Like: &yes})
	}
	f.ok(b, false, application.Command{Action: "vote", EntryID: first, Like: &no})
	f.ok(0, true, application.Command{Action: "finish"})
	if v := f.view(a, false); len(v.Rounds) != 0 || v.Season.WinnerID != nil {
		t.Fatal("results leaked")
	}
	stats := f.view(0, true).Rounds[0].Rows
	if len(stats) != 2 || stats[0].Place != 1 || stats[1].Place != 1 || *stats[0].Percent != 50 || *stats[1].Percent != 100 {
		t.Fatalf("%+v", stats)
	}
	for n := 2; n <= 3; n++ {
		f.ok(0, true, application.Command{Action: "runoff", EntryIDs: []int64{first, second}})
		f.round++
		if f.view(late, false).CanReady {
			t.Fatal("excluded person admitted to runoff")
		}
		f.ok(b, false, application.Command{Action: "ready"})
		f.ok(0, true, application.Command{Action: "start"})
		if n == 3 {
			break
		}
		for _, id := range []int64{first, second} {
			f.ok(b, false, application.Command{Action: "vote", EntryID: id, Like: &yes})
		}
	}
	f.ok(0, true, application.Command{Action: "choose", EntryID: second})
	f.ok(0, true, application.Command{Action: "publish"})
	v := f.view(late, false)
	if len(v.Rounds) != 3 || !v.Rounds[2].Cancelled || v.Season.WinnerID == nil || *v.Season.WinnerID != second {
		t.Fatalf("%+v", v)
	}
	f.checkRead(late, false, "photo-А", true)
}

func TestSingleCandidateAndExpiry(t *testing.T) {
	f := setup(t)
	first := f.entry("A")
	second := f.entry("B")
	a := f.people[0]
	f.ok(a, false, application.Command{Action: "withdraw", EntryID: second})
	requireOK(t, f.ReserveUpload(f.ctx, a, f.season, "aborted"))
	requireOK(t, f.DiscardUpload(f.ctx, a, "aborted"))
	f.moderate(first)
	ids, err := f.UnusedUploads(f.ctx)
	requireOK(t, err)
	if len(ids) != 2 {
		t.Fatalf("expected withdrawn and aborted objects, got %v", ids)
	}
	for _, key := range ids {
		requireOK(t, f.PurgedUpload(f.ctx, key))
	}
	v := f.view(0, true)
	if v.Season.WinnerID == nil || *v.Season.WinnerID != first || v.Rounds[0].Rows[0].Percent != nil {
		t.Fatalf("%+v", v)
	}
	f.ok(0, true, application.Command{Action: "publish"})
}

func TestResultCardAndAdministrativeContacts(t *testing.T) {
	f := setup(t)
	owner, other := f.people[0], f.people[1]
	_, err := f.pool.Exec(f.ctx, `INSERT INTO telegram_identities(person_id,telegram_user_id,username,display_name,last_seen_at)
		VALUES ($1,8001,'author','Author',now()),($2,8002,NULL,'Voter',now())`, owner, other)
	requireOK(t, err)
	entry, rejected := f.entry("Approved"), f.entry("Rejected")
	checkHidden := func(id int64, admin bool) {
		t.Helper()
		if _, err := f.ResultEntry(f.ctx, id, admin); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unexpected result access for %d, admin=%t: %v", id, admin, err)
		}
	}
	checkHidden(entry, true)
	f.moderate(entry)
	author := f.view(0, true).Entries[0]
	if author.AuthorTelegramID == nil || *author.AuthorTelegramID != 8001 || author.AuthorUsername != "author" {
		t.Fatalf("missing administrative contact: %+v", author)
	}
	checkHidden(entry, false)
	checkCard := func(admin bool) {
		t.Helper()
		card, err := f.ResultEntry(f.ctx, entry, admin)
		requireOK(t, err)
		if card.Author != "" || card.AuthorUsername != "" || card.AuthorTelegramID != nil ||
			card.Name != "Approved" || card.Description == "" || len(card.Images) != 1 || !card.Images[0].AI {
			t.Fatalf("unexpected result card: %+v", card)
		}
	}
	checkCard(true)
	f.ok(0, true, application.Command{Action: "publish"})
	checkCard(false)
	f.checkRead(other, false, "photo-Approved", true)
	checkHidden(rejected, true)
	checkHidden(rejected, false)
	requireOK(t, f.Close(f.ctx, f.season))
	checkHidden(entry, true)
	checkHidden(entry, false)
}

func TestBallotPositionsAreBalancedAndStable(t *testing.T) {
	f := setup(t)
	first, second := f.entry("One"), f.entry("Two")
	_, err := f.pool.Exec(f.ctx, `INSERT INTO telegram_identities(person_id,telegram_user_id,display_name,last_seen_at)
		VALUES($1,8001,'Author',now())`, f.people[0])
	requireOK(t, err)
	f.moderate(first, second)
	for _, voter := range f.people[:2] {
		f.ok(voter, false, application.Command{Action: "ready"})
	}
	roster := f.view(0, true).Voters
	if len(roster) != 2 || roster[0].TelegramID == nil || *roster[0].TelegramID != 8001 || roster[0].Username != "" || roster[1].TelegramID != nil {
		t.Fatalf("unexpected voter contacts: %+v", roster)
	}
	f.ok(0, true, application.Command{Action: "start"})
	order := map[int64]int64{}
	for _, voter := range f.people[:2] {
		card := f.view(voter, false).Card
		if card == nil || card.Author != "" || card.AuthorTelegramID != nil || card.AuthorUsername != "" {
			t.Fatalf("expected an anonymous card: %+v", card)
		}
		order[voter] = card.ID
	}
	if order[f.people[0]] == order[f.people[1]] {
		t.Fatal("both voters received the same first candidate")
	}
	f.ok(0, true, application.Command{Action: "start"})
	yes := true
	for voter, id := range order {
		if f.view(voter, false).Card.ID != id {
			t.Fatal("reopening or retrying start reshuffled deck")
		}
		f.ok(voter, false, application.Command{Action: "vote", EntryID: id, Like: &yes})
		if next := f.view(voter, false); next.Remaining != 1 || next.Card.ID == id {
			t.Fatalf("unexpected next card: %+v", next)
		}
	}
	// An in-progress pre-upgrade ballot keeps its old deterministic order.
	person := f.people[0]
	_, err = f.pool.Exec(f.ctx, "UPDATE mascot_voters SET ballot_slot=NULL WHERE person_id=$1", person)
	requireOK(t, err)
	_, err = f.pool.Exec(f.ctx, "DELETE FROM mascot_answers WHERE person_id=$1", person)
	requireOK(t, err)
	var legacyFirst int64
	requireOK(t, f.pool.QueryRow(f.ctx, `SELECT entry_id FROM mascot_candidates
	  WHERE season_id=$1 AND round=$2 ORDER BY md5(concat_ws('/',season_id,round,entry_id,$3::bigint)),entry_id LIMIT 1`,
		f.season, f.round, person).Scan(&legacyFirst))
	if f.view(person, false).Card.ID != legacyFirst {
		t.Fatal("existing ballot order changed")
	}
}

func TestEveryCardPositionIsBalancedWithIncompleteVoterGroup(t *testing.T) {
	f := setup(t)
	var candidates []int64
	for i, author := range f.people {
		var id int64
		requireOK(t, f.pool.QueryRow(f.ctx, `INSERT INTO mascot_entries(season_id,person_id,name,description)
			VALUES($1,$2,$3,'Concept') RETURNING id`, f.season, author, fmt.Sprintf("Concept %d", i)).Scan(&id))
		candidates = append(candidates, id)
	}
	f.moderate(candidates...)
	var voters []int64
	for i := 0; i < 7; i++ {
		var voter int64
		requireOK(t, f.pool.QueryRow(f.ctx, "INSERT INTO people(full_name) VALUES($1) RETURNING id", fmt.Sprintf("Voter %d", i)).Scan(&voter))
		f.ok(voter, false, application.Command{Action: "ready"})
		voters = append(voters, voter)
	}
	f.ok(0, true, application.Command{Action: "start"})
	like := true
	for position := range candidates {
		counts := map[int64]int{}
		for _, voter := range voters {
			v := f.view(voter, false)
			if v.Card == nil || v.Remaining != len(candidates)-position {
				t.Fatalf("incomplete deck in position %d: %+v", position, v)
			}
			counts[v.Card.ID]++
			f.ok(voter, false, application.Command{Action: "vote", EntryID: v.Card.ID, Like: &like})
		}
		for _, id := range candidates {
			if counts[id] < 2 || counts[id] > 3 {
				t.Fatalf("position %d is unbalanced: %v", position, counts)
			}
		}
	}
}

func TestCreateRetryWithChangedContentConflicts(t *testing.T) {
	f := setup(t)
	f.entry("Hoshi")
	err := f.do(f.people[0], false, application.Command{Action: "submit", RequestID: "Hoshi", Name: "Different", Description: "Concept", Images: []domain.Image{{Key: "photo-Hoshi", AI: true}}})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed retry: got %v", err)
	}
	if got := f.view(f.people[0], false).Entries; len(got) != 1 || got[0].Name != "Hoshi" {
		t.Fatalf("changed submission after retry: %+v", got)
	}
}

func TestMigrationReversible(t *testing.T) {
	f := setup(t)
	for _, suffix := range []string{"down", "up"} {
		sql, err := os.ReadFile(filepath.Join("../../../../../migrations", "000018_mascot."+suffix+".sql"))
		requireOK(t, err)
		_, err = f.pool.Exec(f.ctx, string(sql))
		requireOK(t, err)
	}
	_, err := f.Start(f.ctx)
	requireOK(t, err)
}

func TestConcurrentLastVoteAndClosure(t *testing.T) {
	f := setup(t)
	a := f.people[0]
	first, second := f.entry("X"), f.entry("Y")
	f.moderate(first, second)
	f.ok(a, false, application.Command{Action: "ready"})
	f.ok(0, true, application.Command{Action: "start"})
	yes := true
	f.ok(a, false, application.Command{Action: "vote", EntryID: first, Like: &yes})
	start := make(chan struct{})
	ballot := make(chan error, 1)
	closure := make(chan error, 1)
	go func() {
		<-start
		ballot <- f.do(a, false, application.Command{Action: "vote", EntryID: second, Like: &yes})
	}()
	go func() { <-start; closure <- f.do(0, true, application.Command{Action: "finish"}) }()
	close(start)
	voteErr, closeErr := <-ballot, <-closure
	for _, err := range []error{voteErr, closeErr} {
		if err != nil && !errors.Is(err, domain.ErrConflict) {
			t.Fatal(err)
		}
	}
	if voteErr != nil && closeErr != nil {
		t.Fatal("neither action completed")
	}
	v := f.view(0, true)
	if v.Season.Phase != "results" || len(v.Rounds) != 1 {
		t.Fatalf("round did not close: %+v", v)
	}
	count := 0
	for _, row := range v.Rounds[0].Rows {
		count += row.Likes
	}
	if (voteErr == nil && count != 2) || (voteErr != nil && count != 1) {
		t.Fatalf("inconsistent tally: %d, %v", count, voteErr)
	}
	if voteErr == nil {
		f.ok(a, false, application.Command{Action: "vote", EntryID: second, Like: &yes})
	}
}

func TestEligibilityAndExclusiveImageOwnership(t *testing.T) {
	f := setup(t)
	a, b := f.people[0], f.people[1]
	_, err := f.pool.Exec(f.ctx, `WITH registration AS (
   INSERT INTO event_registrations(event_id,person_id,registered_at)
   SELECT id,$1,now() FROM events RETURNING id)
   INSERT INTO attendance(registration_id,attended,recorded_at) SELECT id,true,now() FROM registration`, b)
	requireOK(t, err)
	_, err = f.pool.Exec(f.ctx, "UPDATE events SET starts_at=now()-interval '2 days',ends_at=now()-interval '1 day'")
	requireOK(t, err)
	if f.view(a, false).Eligible || !f.view(b, false).Eligible {
		t.Fatal("eligibility must distinguish registered and attended")
	}
	_, err = f.pool.Exec(f.ctx, "UPDATE events SET starts_at=now()+interval '1 day',ends_at=now()+interval '2 days'")
	requireOK(t, err)
	requireOK(t, f.ReserveUpload(f.ctx, b, f.season, "b-only"))
	requireOK(t, f.FinishUpload(f.ctx, b, "b-only"))
	c := application.Command{Action: "submit", Name: "X", Description: "test", Images: []domain.Image{{Key: "b-only"}}}
	if err := f.do(a, false, c); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	f.ok(b, false, c)
	if v := f.view(a, false); len(v.Entries) != 0 {
		t.Fatal("failed submission was not rolled back")
	}
	f.checkRead(a, false, "b-only", false)
	f.entry("X")
	f.entry("Y")
	requireOK(t, f.ReserveUpload(f.ctx, a, f.season, "Z"))
	requireOK(t, f.FinishUpload(f.ctx, a, "Z"))
	c.Images[0].Key = "Z"
	if err := f.do(a, false, c); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	requireOK(t, f.EndTestSubmissions(f.ctx, f.season))
	if err := f.do(b, false, application.Command{Action: "withdraw", EntryID: f.view(b, false).Entries[0].ID}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
}

func TestPrivateDraftImageLifecycle(t *testing.T) {
	f := setup(t)
	a, b := f.people[0], f.people[1]
	requireOK(t, f.ReserveUpload(f.ctx, a, f.season, "invalid-payload"))
	requireOK(t, f.ReleaseReservation(f.ctx, a, "invalid-payload"))
	if err := f.FinishUpload(f.ctx, a, "invalid-payload"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	requireOK(t, f.ReserveUpload(f.ctx, a, f.season, "photo"))
	requireOK(t, f.FinishUpload(f.ctx, a, "photo"))
	requireOK(t, f.ReleaseReservation(f.ctx, a, "photo"))
	f.checkRead(a, false, "photo", true)
	f.checkRead(a, true, "photo", false) // matching numeric admin Telegram ID cannot grant ownership
	f.checkRead(b, false, "photo", false)
	v := f.view(a, false)
	if len(v.PendingImages) != 1 || v.PendingImages[0] != "photo" {
		t.Fatalf("pending: %v", v.PendingImages)
	}
	if len(f.view(a, true).PendingImages) != 0 || len(f.view(b, false).PendingImages) != 0 {
		t.Fatal("pending image identity leaked")
	}
	if err := f.DiscardOwnedUpload(f.ctx, b, "photo"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	requireOK(t, f.DiscardOwnedUpload(f.ctx, a, "photo"))
	f.checkRead(a, false, "photo", false)
	if len(f.view(a, false).PendingImages) != 0 {
		t.Fatal("discarded upload remains in list")
	}
	f.entry("Y")
	f.checkRead(a, true, "photo-Y", true)
	if err := f.DiscardOwnedUpload(f.ctx, a, "photo-Y"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	f.checkRead(a, false, "photo-Y", true)
	requireOK(t, f.Close(f.ctx, f.season))
	season, err := f.Start(f.ctx)
	requireOK(t, err)
	if err := f.ReserveUpload(f.ctx, a, season.ID, "hidden"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
}
