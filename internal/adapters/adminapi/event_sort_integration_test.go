//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	ep "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/adapters/postgres"
	ea "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	pp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/adapters/postgres"
	people "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
)

func TestEventTableOrderingPostgres(t *testing.T) {
	pool := managementSortTestPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO events(title, starts_at, ends_at) VALUES
		  ('Z','2099-01-01','2099-01-02'),('A','2000-01-01','2000-01-02');
		INSERT INTO people(full_name,is_russian_citizen,is_itmo_student) VALUES
		  ('Y',true,NULL),('Z',true,true),('X',false,true);
		INSERT INTO telegram_identities(person_id,telegram_user_id,username,display_name,last_seen_at) VALUES
		  (1,101,NULL,'Y',NOW()),(2,102,'zulu','Z',NOW()),(3,103,'Alpha','X',NOW());
		INSERT INTO event_registrations(event_id,person_id,registered_at,status,cancelled_at) VALUES
		  (2,1,'2000-01-01','registered',NULL),(2,2,'1999-12-31','registered',NULL),(2,3,'1999-12-30','cancelled','1999-12-31');
		INSERT INTO attendance(registration_id,attended,recorded_at) VALUES (2,true,NOW()),(3,false,NOW());
	`)
	if err != nil {
		t.Fatal(err)
	}
	router := (&Management{Pool: pool}).Router(NewRouter(ea.NewService(ep.NewRepository(pool))))
	check := func(path, field string, expected []int) {
		t.Helper()
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/admin"+path, nil))
		var page struct{ Items []map[string]json.RawMessage }
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &page) != nil {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		var ids []int
		for _, row := range page.Items {
			var id int
			if err := json.Unmarshal(row[field], &id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if !slices.Equal(ids, expected) {
			t.Errorf("%s: got %v want %v", path, ids, expected)
		}
	}
	for _, endpoint := range []string{"/events", "/event-workspace"} {
		for sort, ids := range map[string][]int{
			"id": {1, 2}, "title": {2, 1}, "starts_at": {2, 1},
			"status": {1, 2}, "registrations": {1, 2},
		} {
			check(endpoint+"?sort="+sort, "id", ids)
			check(endpoint+"?sort="+sort+"&order=desc", "id", []int{ids[1], ids[0]})
		}
	}
	check("/events?sort=attended", "id", []int{1, 2})
	if _, err := pool.Exec(ctx, `INSERT INTO publications(id, creator_telegram_id, text, status) VALUES (50, 100, 'Draft', 'draft');
		INSERT INTO events(title,starts_at,ends_at) VALUES ('No visits','2001-01-01','2001-01-02')`); err != nil {
		t.Fatal(err)
	}
	check("/event-workspace?sort=attended", "id", []int{3, 2, 50, 1})
	check("/event-workspace?sort=attended&order=desc", "id", []int{2, 3, 50, 1})
	for sort, ids := range map[string][]int{
		"person_id": {1, 2, 3}, "name": {3, 1, 2}, "telegram": {3, 2, 1},
		"registered_at": {3, 2, 1}, "cancelled_at": {3, 1, 2}, "status": {3, 1, 2},
		"attended": {3, 2, 1}, "needs_pass": {2, 3, 1}, "is_russian_citizen": {3, 1, 2},
		"is_itmo_student": {2, 3, 1},
	} {
		check("/events/2/participants?sort="+sort, "person_id", ids)
	}
	check("/events/2/participants?sort=needs_pass&order=desc", "person_id", []int{3, 2, 1})
}

func TestPeopleTableOrderingPostgres(t *testing.T) {
	pool := managementSortTestPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO people(full_name,is_russian_citizen,is_itmo_student) VALUES
			('Y',true,false),('Z',NULL,true),('X',false,NULL);
		INSERT INTO telegram_identities(person_id,telegram_user_id,display_name,last_seen_at)
			SELECT id,100+id,full_name,NOW() FROM people;
		INSERT INTO feedback(person_id,text) VALUES (1,'One'),(1,'Two'),(3,'Three');
	`)
	if err != nil {
		t.Fatal(err)
	}
	repo := pp.NewRepository(pool)
	for _, tc := range []struct {
		sort  people.SortField
		order people.SortOrder
		ids   []int64
	}{
		{people.SortByID, people.SortDescending, []int64{3, 2, 1}},
		{people.SortByFeedbackCount, people.SortAscending, []int64{2, 3, 1}},
		{people.SortByFeedbackCount, people.SortDescending, []int64{1, 3, 2}},
		{people.SortByCitizen, people.SortAscending, []int64{3, 1, 2}},
		{people.SortByCitizen, people.SortDescending, []int64{1, 3, 2}},
		{people.SortByStudent, people.SortAscending, []int64{1, 2, 3}},
		{people.SortByStudent, people.SortDescending, []int64{2, 1, 3}},
	} {
		list, err := repo.ListPeople(ctx, people.ListPeopleQuery{Sort: tc.sort, Order: tc.order, Page: pagination.Page{Number: 1, Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, item := range list.Items {
			ids = append(ids, item.ID)
		}
		if !slices.Equal(ids, tc.ids) {
			t.Errorf("sort %s %s: got %v want %v", tc.sort, tc.order, ids, tc.ids)
		}
	}
}
