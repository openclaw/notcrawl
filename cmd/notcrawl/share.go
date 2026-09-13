package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/share"
	"github.com/openclaw/notcrawl/internal/store"
)

func runPublish(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	remote := fs.String("remote", cfg.Share.Remote, "git remote")
	repo := fs.String("repo", cfg.Share.RepoPath, "share repo path")
	branch := fs.String("branch", cfg.Share.Branch, "share branch")
	message := fs.String("message", "archive: notcrawl snapshot", "commit message")
	tag := fs.String("tag", "", "immutable snapshot tag")
	push := fs.Bool("push", false, "push after commit")
	noCommit := fs.Bool("no-commit", false, "write snapshot without committing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := (markdown.Exporter{Store: st, Dir: cfg.MarkdownDir}).Export(ctx); err != nil {
		return err
	}
	s, err := share.Publish(ctx, st, share.PublishOptions{
		RepoPath: *repo, Remote: *remote, Branch: *branch, MarkdownDir: cfg.MarkdownDir,
		Message: *message, Push: *push, Commit: !*noCommit, Tag: *tag,
	})
	if err != nil {
		return err
	}
	if s.Tag == "" {
		fmt.Fprintf(stdout, "published %d tables to %s committed=%t pushed=%t\n", len(s.Manifest.Tables), *repo, s.Committed, s.Pushed)
	} else {
		fmt.Fprintf(stdout, "published %d tables to %s committed=%t pushed=%t tag=%s\n", len(s.Manifest.Tables), *repo, s.Committed, s.Pushed, s.Tag)
	}
	return nil
}

func runSubscribe(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("subscribe", flag.ContinueOnError)
	fs.SetOutput(stdout)
	repo := fs.String("repo", cfg.Share.RepoPath, "share repo path")
	branch := fs.String("branch", cfg.Share.Branch, "share branch")
	restore := fs.Bool("restore", false, "replace the local archive exactly, removing rows absent from the snapshot")
	retainRevisions := fs.Bool("retain-revisions", false, "save replaced local rows in record_revisions")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	remote := cfg.Share.Remote
	if fs.NArg() > 0 {
		remote = fs.Arg(0)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	result, err := share.SubscribeWithOptions(ctx, st, remote, *repo, *branch, share.ImportOptions{
		Restore:         *restore,
		RetainRevisions: *retainRevisions,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "subscribed %s mode=%s revisions=%d tables=%d generated_at=%s\n",
		remote, result.Mode, result.Revisions, len(result.Manifest.Tables), result.Manifest.GeneratedAt)
	return nil
}

func runUpdate(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stdout)
	repo := fs.String("repo", cfg.Share.RepoPath, "share repo path")
	branch := fs.String("branch", cfg.Share.Branch, "share branch")
	ref := fs.String("ref", "", "historical tag, commit, or branch")
	restore := fs.Bool("restore", false, "replace the local archive exactly, removing rows absent from the snapshot")
	retainRevisions := fs.Bool("retain-revisions", false, "save replaced local rows in record_revisions")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	result, resolvedRef, err := share.UpdateAtWithOptions(ctx, st, cfg.Share.Remote, *repo, *branch, *ref, share.ImportOptions{
		Restore:         *restore,
		RetainRevisions: *retainRevisions,
	})
	if err != nil {
		return err
	}
	if resolvedRef == "" {
		fmt.Fprintf(stdout, "updated mode=%s revisions=%d tables=%d generated_at=%s\n",
			result.Mode, result.Revisions, len(result.Manifest.Tables), result.Manifest.GeneratedAt)
	} else {
		fmt.Fprintf(stdout, "updated ref=%s mode=%s revisions=%d tables=%d generated_at=%s\n",
			resolvedRef, result.Mode, result.Revisions, len(result.Manifest.Tables), result.Manifest.GeneratedAt)
	}
	return nil
}
