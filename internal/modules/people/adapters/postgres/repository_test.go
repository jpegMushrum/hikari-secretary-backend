package postgres

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
)

func TestPeopleWhereClauseUsesNumberedParametersForEveryFilter(t *testing.T) {
	citizen := true
	itmo := false
	where, args := peopleWhereClause(application.ListPeopleQuery{
		Search:           "Hikari",
		IsRussianCitizen: &citizen,
		IsITMOStudent:    &itmo,
	})

	for _, expected := range []string{
		"p.full_name ILIKE $1",
		"ti.username ILIKE $1",
		"ti.display_name ILIKE $1",
		"p.is_russian_citizen = $2",
		"p.is_itmo_student = $3",
	} {
		if !strings.Contains(where, expected) {
			t.Errorf("where clause %q does not contain %q", where, expected)
		}
	}
	if strings.Contains(where, "%!") {
		t.Fatalf("where clause contains a formatting error: %q", where)
	}
	if want := []any{"%Hikari%", true, false}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %#v, want %#v", args, want)
	}
}

func TestPeopleOrderClauseOnlyUsesStaticSQLFragments(t *testing.T) {
	query := application.ListPeopleQuery{Sort: application.SortByVisits, Order: application.SortAscending}
	if got, want := peopleOrderClause(query), "COALESCE(am.visits, 0) ASC, p.id ASC"; got != want {
		t.Errorf("peopleOrderClause() = %q, want %q", got, want)
	}
}
