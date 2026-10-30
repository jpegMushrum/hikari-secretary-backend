package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/domain"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) SyncTelegramIdentity(ctx context.Context, command domain.SyncIdentityCommand) (domain.TelegramIdentity, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.TelegramIdentity{}, fmt.Errorf("begin identity sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", command.TelegramUserID); err != nil {
		return domain.TelegramIdentity{}, fmt.Errorf("lock Telegram identity: %w", err)
	}

	var personID int64
	err = tx.QueryRow(ctx, `
		SELECT person_id
		FROM telegram_identities
		WHERE telegram_user_id = $1
		FOR UPDATE`, command.TelegramUserID).Scan(&personID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, "INSERT INTO people DEFAULT VALUES RETURNING id").Scan(&personID); err != nil {
			return domain.TelegramIdentity{}, fmt.Errorf("create person for Telegram identity: %w", err)
		}
	} else if err != nil {
		return domain.TelegramIdentity{}, fmt.Errorf("find Telegram identity: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO telegram_identities (
			person_id, telegram_user_id, username, display_name, last_seen_at
		) VALUES ($1, $2, NULLIF($3, ''), $4, NOW())
		ON CONFLICT (telegram_user_id) DO UPDATE
		SET username = EXCLUDED.username,
			display_name = EXCLUDED.display_name,
			last_seen_at = NOW(),
			updated_at = NOW()`, personID, command.TelegramUserID, command.Username, command.DisplayName); err != nil {
		return domain.TelegramIdentity{}, fmt.Errorf("upsert Telegram identity: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramIdentity{}, fmt.Errorf("commit identity sync: %w", err)
	}

	return domain.TelegramIdentity{
		PersonID:       personID,
		TelegramUserID: command.TelegramUserID,
		Username:       command.Username,
		DisplayName:    command.DisplayName,
	}, nil
}

func (r *Repository) ListPeople(ctx context.Context, query application.ListPeopleQuery) (application.ListPeopleResult, error) {
	where, args := peopleWhereClause(query)

	var total int64
	countSQL := `
		SELECT COUNT(*)
		FROM people p
		JOIN telegram_identities ti ON ti.person_id = p.id
		` + where
	if err := r.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return application.ListPeopleResult{}, fmt.Errorf("count people: %w", err)
	}

	limitPosition := len(args) + 1
	offsetPosition := len(args) + 2
	listSQL := `
		WITH attendance_metrics AS (
			SELECT er.person_id,
				COUNT(*) FILTER (WHERE a.attended) AS visits,
				MAX(e.starts_at) FILTER (WHERE a.attended) AS last_visit_at
			FROM event_registrations er
			LEFT JOIN attendance a ON a.registration_id = er.id
			LEFT JOIN events e ON e.id = er.event_id
			GROUP BY er.person_id
		), feedback_metrics AS (
			SELECT person_id, COUNT(*) AS feedback_count
			FROM feedback
			GROUP BY person_id
		)
		SELECT p.id, p.full_name, ti.telegram_user_id, ti.username, ti.display_name,
			p.is_russian_citizen, p.is_itmo_student,
			COALESCE(am.visits, 0), COALESCE(fm.feedback_count, 0), am.last_visit_at
		FROM people p
		JOIN telegram_identities ti ON ti.person_id = p.id
		LEFT JOIN attendance_metrics am ON am.person_id = p.id
		LEFT JOIN feedback_metrics fm ON fm.person_id = p.id
		` + where + `
		ORDER BY ` + peopleOrderClause(query) + fmt.Sprintf("\n\t\tLIMIT $%d OFFSET $%d", limitPosition, offsetPosition)

	rows, err := r.pool.Query(ctx, listSQL, append(args, query.Page.Size, query.Page.Offset())...)
	if err != nil {
		return application.ListPeopleResult{}, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close()

	items := make([]application.PersonListItem, 0, query.Page.Size)
	for rows.Next() {
		var item application.PersonListItem
		if err := rows.Scan(
			&item.ID,
			&item.FullName,
			&item.TelegramUserID,
			&item.Username,
			&item.DisplayName,
			&item.IsRussianCitizen,
			&item.IsITMOStudent,
			&item.Visits,
			&item.FeedbackCount,
			&item.LastVisitAt,
		); err != nil {
			return application.ListPeopleResult{}, fmt.Errorf("scan person: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return application.ListPeopleResult{}, fmt.Errorf("iterate people: %w", err)
	}

	return application.ListPeopleResult{Items: items, TotalItems: total}, nil
}

func peopleWhereClause(query application.ListPeopleQuery) (string, []any) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)
	add := func(clause string, value any) {
		position := len(args) + 1
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, position))
	}

	if search := strings.TrimSpace(query.Search); search != "" {
		add(`(
			p.full_name ILIKE $%[1]d
			OR ti.username ILIKE $%[1]d
			OR ti.display_name ILIKE $%[1]d
		)`, "%"+search+"%")
	}
	if query.IsRussianCitizen != nil {
		add("p.is_russian_citizen = $%d", *query.IsRussianCitizen)
	}
	if query.IsITMOStudent != nil {
		add("p.is_itmo_student = $%d", *query.IsITMOStudent)
	}

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func peopleOrderClause(query application.ListPeopleQuery) string {
	field := query.Sort
	if field == "" {
		field = application.SortByName
	}

	order := query.Order
	if order == "" {
		if field == application.SortByName {
			order = application.SortAscending
		} else {
			order = application.SortDescending
		}
	}
	direction := "ASC"
	if order == application.SortDescending {
		direction = "DESC"
	}

	switch field {
	case application.SortByVisits:
		return "COALESCE(am.visits, 0) " + direction + ", p.id " + direction
	case application.SortByLastVisit:
		return "am.last_visit_at " + direction + " NULLS LAST, p.id " + direction
	default:
		return "COALESCE(p.full_name, ti.display_name) " + direction + ", p.id " + direction
	}
}
