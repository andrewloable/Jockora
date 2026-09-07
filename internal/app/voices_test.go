// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Preview renders through the SAME sidecar the DJ uses, so what an operator
// hears is what a listener will hear.

func TestSidecarPreviewSpeaks(t *testing.T) {
	var gotBody []byte
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFfake"))
	}))
	defer srv.Close()

	audio, err := newSidecarVoices(srv.URL).Preview(context.Background(), "af_heart", "Good evening.")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if string(audio) != "RIFFfake" {
		t.Errorf("audio = %q", audio)
	}
	if gotPath != "/synth" {
		t.Errorf("path = %q, want /synth", gotPath)
	}
	if !strings.Contains(string(gotBody), `"voice":"af_heart"`) ||
		!strings.Contains(string(gotBody), `"text":"Good evening."`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestSidecarPreviewStripsThePrefix(t *testing.T) {
	// A jock card carries "kokoro:af_heart"; the sidecar wants the bare name.
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte("wav"))
	}))
	defer srv.Close()

	if _, err := newSidecarVoices(srv.URL).Preview(context.Background(), "kokoro:af_heart", "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotBody), `"voice":"af_heart"`) {
		t.Errorf("prefix was not stripped: %s", gotBody)
	}
}

func TestSidecarPreviewReportsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := newSidecarVoices(srv.URL).Preview(context.Background(), "x", "hi"); err == nil {
		t.Error("a 500 from the sidecar was treated as audio")
	}
}

func TestSidecarPreviewReportsAnUnreachableSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if _, err := newSidecarVoices(url).Preview(context.Background(), "x", "hi"); err == nil {
		t.Error("an unreachable sidecar was treated as audio")
	}
}

func TestSidecarPreviewRejectsABadURL(t *testing.T) {
	if _, err := newSidecarVoices("http://\x7f").Preview(context.Background(), "x", "hi"); err == nil {
		t.Error("a malformed base url was accepted")
	}
}

// Voices is the list the console's picker is built from. It had no test, and
// it was broken in every managed-sidecar deployment: the client was pointed at
// the CONFIGURED address while the sidecar runs on the port its supervisor
// assigned, so the picker was empty and said nothing about why.

func TestSidecarVoicesLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/voices" {
			t.Errorf("asked for %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"voices":["af_heart","am_fenrir"]}`))
	}))
	defer srv.Close()

	got, err := newSidecarVoices(srv.URL + "/").Voices(context.Background())
	if err != nil {
		t.Fatalf("Voices: %v", err)
	}
	if len(got) != 2 || got[0] != "af_heart" {
		t.Errorf("voices = %v", got)
	}
}

func TestSidecarVoicesReportsAnUnreachableSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if _, err := newSidecarVoices(url).Voices(context.Background()); err == nil {
		t.Error("an unreachable sidecar produced a voice list")
	}
}

func TestSidecarVoicesReportsRefusalAndNonsense(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := newSidecarVoices(bad.URL).Voices(context.Background()); err == nil {
		t.Error("a 500 was treated as a voice list")
	}

	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer junk.Close()
	if _, err := newSidecarVoices(junk.URL).Voices(context.Background()); err == nil {
		t.Error("junk was treated as a voice list")
	}
}

func TestSidecarVoicesRejectsABadURL(t *testing.T) {
	if _, err := newSidecarVoices("http://\x7f").Voices(context.Background()); err == nil {
		t.Error("a malformed base url was accepted")
	}
}

func TestSidecarPreviewReportsATruncatedBody(t *testing.T) {
	// Content-Length promises more than the sidecar delivers: the read fails
	// part way, and half a WAV must not be handed back as audio.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	if _, err := newSidecarVoices(srv.URL).Preview(context.Background(), "x", "hi"); err == nil {
		t.Error("a truncated body was treated as audio")
	}
}
