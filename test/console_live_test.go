// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build console

// A seeded console on localhost, for measuring what a rule actually does.
//
// TAGGED OFF because it needs a built web/dist and a browser binary, neither of
// which CI has. It is a workbench, not a gate: run it, point a browser at the
// address it prints, measure, stop it.
package test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

func TestConsoleLive(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(root, "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(gateAdminPassword, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, gateAdmin, hash, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	// A library with long album titles, because the reported defect is Album
	// wrapping three deep beside two empty columns.
	for i := 1; i <= 60; i++ {
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO tracks (id, path, playable, artist, title, album, year, duration_s)
			VALUES (?, ?, 1, ?, ?, '100 Greatest Rock Songs of the 90s', 1994, 240)`,
			i, fmt.Sprintf("/m/%d.mp3", i),
			fmt.Sprintf("Artist Number %d", i), fmt.Sprintf("A Song With Quite A Long Title %d", i),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO dossiers (track_id, json, confidence, created_at)
			VALUES (?, '{"station_tags":["rock","alternative"],"mood":["defiant"]}', 'high', ?)`,
			i, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateAd(ctx, store.Ad{
		Brand: "Stillwater Ceramics", Delivery: "warm",
		Brief:  "Hand-thrown mugs, made in a shed in Bacolod. Open Saturdays.",
		Script: "Stillwater Ceramics. One mug, thrown by hand, open Saturdays in Bacolod.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAd(ctx, store.Ad{
		Brand: "Harrow Books", Delivery: "dry",
		Brief:  "A second-hand bookshop with a cat and no computer at all.",
		Script: "Harrow Books. Books, a cat, and not one computer on the premises.",
	}); err != nil {
		t.Fatal(err)
	}

	logs := &bytes.Buffer{}
	a, err := app.New(&config.Config{
		ListenAddr: "127.0.0.1:42099", SegmentDir: filepath.Join(root, "segments"),
		SampleRate: mix.SampleRate, Channels: mix.Channels, SegmentSeconds: 2, ListSize: 10,
		AdEveryNBreaks: 4,
	}, app.Options{
		Library: &app.Library{Store: st, Selector: station.NewSelector([]int64{1}, 1)},
		LLM:     enrich.NewSwitchable(nil),
		Log:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && a.Addr() == ""; {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Addr() == "" {
		t.Fatal("the server never came up")
	}
	fmt.Printf("CONSOLE_READY http://%s %s %s\n", a.Addr(), gateAdmin, gateAdminPassword)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	select {
	case <-sig:
	case <-time.After(55 * time.Minute):
	}
	cancel()
	<-done
	st.Close()
	t.Logf("server log:\n%s", logs.String())
}
