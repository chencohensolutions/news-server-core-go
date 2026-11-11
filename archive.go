package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/backup"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// archiveNow adds the round's new rows to the archive folder; a failure is logged, and the next round tries again.
func archiveNow(ctx context.Context, db *store.Store, dir string) {
	start := time.Now()
	result, err := backup.Archive(ctx, db, dir, time.Now())
	if err != nil {
		log.Printf("archive: %v", err)
		return
	}
	log.Printf("archive: %s, in %s", result, time.Since(start).Round(time.Millisecond))
}

// archiveCommand is the archive command: bring the archive folder up to date now.
func archiveCommand(config auth.AppConfig, out io.Writer) error {
	if config.ArchiveDir == "-" {
		return fmt.Errorf("the archive is off (archive_dir is \"-\")")
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := backup.Archive(context.Background(), db, config.ArchiveDir, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "archived into %s: %s\n", config.ArchiveDir, result)
	return nil
}

// restoreCommand is the restore command: a new database_file from the archive folder. It refuses to touch a
// database that is there already.
func restoreCommand(config auth.AppConfig, out io.Writer) error {
	if config.ArchiveDir == "-" {
		return fmt.Errorf("the archive is off (archive_dir is \"-\")")
	}
	if _, err := os.Stat(config.DatabaseFile); err == nil {
		return fmt.Errorf("%s is there already: move it away first, restore makes a new one", config.DatabaseFile)
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	start := time.Now()
	result, err := backup.Restore(context.Background(), db, config.ArchiveDir, func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "restored %s from %s in %s:", config.DatabaseFile, config.ArchiveDir, time.Since(start).Round(time.Second))
	for table, n := range result.Rows {
		fmt.Fprintf(out, " %d %s", n, table)
	}
	fmt.Fprintln(out, "\nThe story vectors are made again when the server starts.")
	return nil
}
