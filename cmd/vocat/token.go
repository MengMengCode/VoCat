package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"vocat/internal/auth"
	"vocat/internal/store"
)

// runToken manages bearer credentials against the same database the server
// uses. Tokens are issued here and only here; the server never prints them.
func runToken(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: vocat token <create|list|revoke> [flags] <name>")
	}
	switch args[0] {
	case "create":
		return runTokenCreate(args[1:], stdout)
	case "list":
		return runTokenList(args[1:], stdout)
	case "revoke":
		return runTokenRevoke(args[1:], stdout)
	default:
		return fmt.Errorf("unknown token subcommand %q; want create, list, or revoke", args[0])
	}
}

func runTokenCreate(args []string, stdout io.Writer) error {
	return runTokenCreateWithDependencies(args, stdout, appendTokenAudit, func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 15*time.Second)
	})
}

func runTokenCreateWithDependencies(args []string, stdout io.Writer, appendAudit func(context.Context, *store.Store, string, string) error, newContext func() (context.Context, context.CancelFunc)) error {
	flags := flag.NewFlagSet("token create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", "/opt/vocat/data/vocat.db", "database path")
	readOnly := flags.Bool("read-only", false, "token may only read")
	ttl := flags.Duration("ttl", 0, "token lifetime, e.g. 720h; zero never expires")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return errors.New("usage: vocat token create [--database path] [--read-only] [--ttl 720h] <name>")
	}
	name := strings.TrimSpace(flags.Arg(0))
	if *ttl < 0 {
		return errors.New("--ttl must be zero (never expires) or a positive duration")
	}
	ctx, cancel := newContext()
	defer cancel()
	database, service, err := openTokenService(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	credentials, err := service.CreateAPIToken(ctx, name, *readOnly, *ttl)
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, database, "auth.token.create", credentials.Name); err != nil {
		// The credential was never shown; without cleanup the name would be
		// stuck behind a token nobody can use. Detach cleanup from the request
		// deadline while keeping it bounded.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		if revokeErr := service.RevokeAPIToken(cleanupCtx, credentials.Name); revokeErr != nil {
			return fmt.Errorf("%w (also failed to roll back the stored token: %v)", err, revokeErr)
		}
		return err
	}
	expiry := "never"
	if !credentials.ExpiresAt.IsZero() {
		expiry = credentials.ExpiresAt.Format(time.RFC3339)
	}
	mode := "read-write"
	if credentials.ReadOnly {
		mode = "read-only"
	}
	fmt.Fprintf(stdout, "token: %s\n", credentials.Token)
	fmt.Fprintf(stdout, "name: %s (%s, expires: %s)\n", credentials.Name, mode, expiry)
	fmt.Fprintln(stdout, "store this token now; it cannot be shown again")
	return nil
}

func runTokenList(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("token list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", "/opt/vocat/data/vocat.db", "database path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: vocat token list [--database path]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	database, service, err := openTokenService(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	tokens, err := service.ListAPITokens(ctx)
	if err != nil {
		return err
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tREAD-ONLY\tCREATED\tLAST-USED\tEXPIRES")
	for _, token := range tokens {
		readOnly := "no"
		if token.ReadOnly {
			readOnly = "yes"
		}
		lastUsed := "never"
		if !token.LastUsedAt.IsZero() {
			lastUsed = token.LastUsedAt.Format(time.RFC3339)
		}
		expires := "never"
		if !token.ExpiresAt.IsZero() {
			expires = token.ExpiresAt.Format(time.RFC3339)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			token.Name, readOnly, token.CreatedAt.Format(time.RFC3339), lastUsed, expires)
	}
	return writer.Flush()
}

func runTokenRevoke(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", "/opt/vocat/data/vocat.db", "database path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return errors.New("usage: vocat token revoke [--database path] <name>")
	}
	name := strings.TrimSpace(flags.Arg(0))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	database, service, err := openTokenService(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := service.RevokeAPIToken(ctx, name); err != nil {
		return err
	}
	if err := appendTokenAudit(ctx, database, "auth.token.revoke", name); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "revoked %s\n", name)
	return nil
}

func openTokenService(ctx context.Context, path string) (*store.Store, *auth.Service, error) {
	database, err := store.Open(ctx, strings.TrimSpace(path))
	if err != nil {
		return nil, nil, err
	}
	service, err := auth.New(database, auth.Options{SessionTTL: 24 * time.Hour})
	if err != nil {
		_ = database.Close()
		return nil, nil, err
	}
	return database, service, nil
}

func appendTokenAudit(ctx context.Context, database *store.Store, action string, name string) error {
	_, err := database.AppendAuditEvent(ctx, store.AuditEvent{
		Actor:      "cli",
		Action:     action,
		EntityType: "api_token",
		EntityID:   name,
		Outcome:    "success",
		RemoteAddr: "local",
		CreatedAt:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}
