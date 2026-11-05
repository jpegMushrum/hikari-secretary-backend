package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/legacyimport"
)

func main() {
	var sqlitePath string
	var jsonOutput bool
	var apply bool
	var databaseURL string
	flag.StringVar(&sqlitePath, "sqlite", "", "path to the legacy SQLite database")
	flag.BoolVar(&jsonOutput, "json", false, "write the report as JSON")
	flag.BoolVar(&apply, "apply", false, "apply the validated import to PostgreSQL")
	flag.StringVar(&databaseURL, "database-url", os.Getenv("DATABASE_URL"), "target PostgreSQL connection URL (required with --apply)")
	flag.Parse()

	if sqlitePath == "" {
		fmt.Fprintln(os.Stderr, "--sqlite is required")
		os.Exit(2)
	}

	var report legacyimport.Report
	var err error
	if apply {
		report, err = legacyimport.Apply(context.Background(), sqlitePath, databaseURL)
	} else {
		report, err = legacyimport.DryRun(context.Background(), sqlitePath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "legacy import failed: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
	} else {
		printReport(report)
	}

	if !report.Valid {
		os.Exit(2)
	}
}

func printReport(report legacyimport.Report) {
	status := "VALID"
	if !report.Valid {
		status = "INVALID"
	}
	label := "Dry-run import"
	if report.Applied {
		label = "Applied import"
	}
	fmt.Printf("%s: %s\n", label, status)
	fmt.Printf("Source: %s (schema version %d)\n", report.SourceSQLitePath, report.SourceVersion)
	fmt.Printf("Source counts: profiles=%d events=%d registrations=%d feedback=%d\n", report.Counts.Profiles, report.Counts.Events, report.Counts.Registrations, report.Counts.Feedback)
	fmt.Printf("Projected PostgreSQL counts: people=%d identities=%d events=%d registrations=%d attendance=%d feedback=%d\n", report.Counts.ProjectedPeople, report.Counts.ProjectedTelegramIdentities, report.Counts.Events, report.Counts.Registrations, report.Counts.ProjectedAttendances, report.Counts.Feedback)
	for _, issue := range report.Issues {
		fmt.Printf("- %s", issue.Table)
		if issue.LegacyID != "" {
			fmt.Printf(" #%s", issue.LegacyID)
		}
		if issue.Field != "" {
			fmt.Printf(" (%s)", issue.Field)
		}
		fmt.Printf(": %s — %s\n", issue.Code, issue.Message)
	}
}
