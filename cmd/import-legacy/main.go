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
	flag.StringVar(&sqlitePath, "sqlite", "", "path to the legacy SQLite database")
	flag.BoolVar(&jsonOutput, "json", false, "write the report as JSON")
	flag.Parse()

	if sqlitePath == "" {
		fmt.Fprintln(os.Stderr, "--sqlite is required")
		os.Exit(2)
	}

	report, err := legacyimport.DryRun(context.Background(), sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dry-run import failed: %v\n", err)
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
	fmt.Printf("Dry-run import: %s\n", status)
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
