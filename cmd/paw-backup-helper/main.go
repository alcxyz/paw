// paw-backup-helper runs only in an operator-created, stopped-workspace helper
// pod. It has no Kubernetes client, provider, shell, or credential integration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alcxyz/paw/internal/backup"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "serve" {
		<-ctx.Done()
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "backup helper requires an operator-selected operation")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	switch args[0] {
	case "check-empty":
		if len(args) != 1 {
			return 2
		}
		for _, root := range []string{"/backup/state", "/backup/work"} {
			info, err := os.Lstat(root)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return 1
			}
			directory, err := os.Open(root)
			if err != nil {
				return 1
			}
			_, readErr := directory.Readdirnames(1)
			closeErr := directory.Close()
			if !errors.Is(readErr, io.EOF) || closeErr != nil {
				return 1
			}
		}
	case "export":
		flags := flag.NewFlagSet("export", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var metadata backup.Metadata
		flags.StringVar(&metadata.Image, "image", "", "original runtime reference")
		flags.StringVar(&metadata.ImageID, "image-id", "", "original runtime identity")
		flags.StringVar(&metadata.Profile, "profile", "", "workspace profile")
		flags.StringVar(&metadata.Provider, "provider", "", "workspace provider")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "invalid backup helper options")
			return 2
		}
		if err := backup.Write(ctx, stdout, backup.WriteRequest{
			Metadata: metadata, StateRoot: "/backup/state", WorkRoot: "/backup/work",
		}); err != nil {
			fmt.Fprintln(stderr, "backup export failed; diagnostics suppressed")
			return 1
		}
	case "import":
		if len(args) != 1 {
			return 2
		}
		if _, err := backup.Restore(ctx, stdin, backup.RestoreRequest{
			StateRoot: "/backup/state", WorkRoot: "/backup/work",
		}); err != nil {
			fmt.Fprintln(stderr, "backup restore failed; destination remains stopped; diagnostics suppressed")
			return 1
		}
	default:
		fmt.Fprintln(stderr, "unsupported backup helper operation")
		return 2
	}
	return 0
}
