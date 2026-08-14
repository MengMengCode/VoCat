package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"vocat/internal/auth"
	"vocat/internal/store"
)

func runBootstrapAdmin(args []string) error {
	flags := flag.NewFlagSet("bootstrap-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", "/opt/vocat/data/vocat.db", "database path")
	username := flags.String("username", "admin", "administrator username")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: vocat bootstrap-admin [--database path] [--username name]")
	}
	reader := bufio.NewReader(os.Stdin)
	password, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read password: %w", err)
	}
	password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
	if password == "" {
		return errors.New("bootstrap password cannot be empty")
	}
	adminUsername := strings.TrimSpace(*username)
	if len(adminUsername) < 1 || len(adminUsername) > 64 || strings.ContainsAny(adminUsername, "\r\n\t") {
		return errors.New("bootstrap username must contain between 1 and 64 characters without control whitespace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	database, err := store.Open(ctx, strings.TrimSpace(*databasePath))
	if err != nil {
		return err
	}
	defer database.Close()
	service, err := auth.New(database, auth.Options{SessionTTL: 24 * time.Hour})
	if err != nil {
		return err
	}
	created, err := service.EnsureAdminIfMissing(ctx, adminUsername, password)
	if err != nil {
		return err
	}
	if created {
		fmt.Println("created")
	} else {
		fmt.Println("exists")
	}
	return nil
}
