// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// fakeLLM returns canned replies in order and records the prompts it saw.
type fakeLLM struct {
	replies []Completion
	errs    []error
	prompts []string
	schemas []map[string]any
	calls   int
}

func (f *fakeLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	f.prompts = append(f.prompts, req.Prompt)
	f.schemas = append(f.schemas, req.JSONSchema)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return Completion{}, f.errs[i]
	}
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	return Completion{}, errors.New("fakeLLM: no more replies")
}

func ok(content string) Completion {
	return Completion{Content: content, StopType: "eos"}
}

func goodJSON() string {
	return `{"station_tags":["synthwave"],"mood":["nocturnal"],"themes":["night driving"],
	"subject_summary":"A song about driving at night.","artist_facts":["Formed in 1980."],
	"notable_line":"headlights","sources":["musicbrainz"],"confidence":"high"}`
}

func input() TrackInput {
	return TrackInput{
		Artist: "New Order", Title: "Blue Monday", Album: "Power, Corruption & Lies",
		Year: 1983, DurationS: 442,
		ArtistFacts: ArtistFacts{Found: true, Name: "New Order", Country: "GB",
			Type: "Group", BeginYear: 1980, Source: SourceMusicBrainz},
		HasSyncedLyrics: true,
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB().Exec(`INSERT INTO tracks (id, path) VALUES (1, '/music/a.mp3')`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDossierValidJSONIsStored(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(goodJSON())}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatalf("GenerateDossier: %v", err)
	}
	if d.Confidence != ConfidenceHigh {
		t.Errorf("Confidence = %q, want high", d.Confidence)
	}
	if len(d.StationTags) != 1 || d.StationTags[0] != "synthwave" {
		t.Errorf("StationTags = %v", d.StationTags)
	}

	s := openStore(t)
	if err := StoreDossier(context.Background(), s, 1, d); err != nil {
		t.Fatal(err)
	}
	var raw, conf string
	if err := s.DB().QueryRow(`SELECT json, confidence FROM dossiers WHERE track_id = 1`).
		Scan(&raw, &conf); err != nil {
		t.Fatalf("dossier row missing: %v", err)
	}
	if conf != ConfidenceHigh {
		t.Errorf("stored confidence = %q", conf)
	}
	var back Dossier
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Errorf("stored json does not parse: %v", err)
	}
}

func TestDossierMalformedJSONRetriesOnce(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok("this is not json at all"), ok(goodJSON())}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatalf("GenerateDossier: %v", err)
	}
	if llm.calls != 2 {
		t.Errorf("made %d calls, want exactly 2 (one failure, one retry)", llm.calls)
	}
	if d.Confidence != ConfidenceHigh {
		t.Errorf("Confidence = %q after a successful retry", d.Confidence)
	}
}

// TestDossierMalformedTwiceStoresEmptyDossier: one bad track must not abort a
// ten-thousand-track run, and it must leave a ROW rather than a gap, or the
// enrichment queue will retry it forever.
func TestDossierMalformedTwiceStoresEmptyDossier(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok("garbage"), ok("still garbage")}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatalf("two bad responses should not be an error: %v", err)
	}
	if d.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want none", d.Confidence)
	}
	if len(d.ArtistFacts) != 0 || d.SubjectSummary != "" {
		t.Errorf("an unparseable response produced content: %+v", d)
	}

	s := openStore(t)
	if err := StoreDossier(context.Background(), s, 1, d); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM dossiers WHERE track_id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d dossier rows, want 1: a failed track still needs a row", n)
	}
}

func TestDossierEmptyResponseIsDistinctFromMalformed(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{{Content: "", StopType: "eos"}}}

	_, err := GenerateDossier(context.Background(), llm, input())
	if !errors.Is(err, ErrLLMEmpty) {
		t.Fatalf("err = %v, want ErrLLMEmpty", err)
	}
	if errors.Is(err, ErrLLMBadJSON) {
		t.Error("an empty response was classified as bad JSON")
	}
}

// TestDossierRefusalIsDistinctFromMalformed: a refusal on every track means the
// persona prompt is tripping model guardrails. Collapsed into bad-JSON, that
// costs a week debugging the parser instead of the prompt.
func TestDossierRefusalIsDistinctFromMalformed(t *testing.T) {
	for _, phrase := range []string{
		"I can't help with that.",
		"I cannot provide information about this.",
		"I'm unable to assist with this request.",
		"As an AI language model, I am not able to",
	} {
		llm := &fakeLLM{replies: []Completion{ok(phrase)}}
		_, err := GenerateDossier(context.Background(), llm, input())
		if !errors.Is(err, ErrLLMRefusal) {
			t.Errorf("%q gave err = %v, want ErrLLMRefusal", phrase, err)
		}
		if errors.Is(err, ErrLLMBadJSON) {
			t.Errorf("%q was classified as bad JSON", phrase)
		}
	}
}

func TestDossierTruncationIsErrLLMEmpty(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{{Content: `{"station_tags":["synth`, StopType: "limit"}}}

	_, err := GenerateDossier(context.Background(), llm, input())
	if !errors.Is(err, ErrLLMEmpty) {
		t.Fatalf("err = %v, want ErrLLMEmpty for a truncated response", err)
	}
	if errors.Is(err, ErrLLMBadJSON) {
		t.Error("truncation was classified as bad JSON; the token budget is the real problem")
	}
}

func TestDossierNoSourcesYieldsConfidenceNone(t *testing.T) {
	in := input()
	in.ArtistFacts = ArtistFacts{} // nothing from MusicBrainz
	in.HasSyncedLyrics = false

	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["synthwave"],"mood":[],"themes":[],
		"subject_summary":"","artist_facts":["invented fact"],"notable_line":"",
		"sources":[],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q with no sources at all, want none", d.Confidence)
	}
	if len(d.ArtistFacts) != 0 {
		t.Errorf("ArtistFacts = %v with no sources; the model invented them and they must be dropped", d.ArtistFacts)
	}
}

func TestDossierStationTagsRestrictedToVocabulary(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["synthwave","vaporwave"],
		"mood":["nocturnal"],"themes":[],"subject_summary":"x","artist_facts":[],
		"notable_line":"","sources":["musicbrainz"],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.StationTags) != 1 || d.StationTags[0] != "synthwave" {
		t.Errorf("StationTags = %v, want only synthwave: vaporwave is out of vocabulary", d.StationTags)
	}
}

func TestDossierMoodRestrictedToVocabulary(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["synthwave"],
		"mood":["melancholic","sad"],"themes":[],"subject_summary":"x","artist_facts":[],
		"notable_line":"","sources":["musicbrainz"],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Mood) != 1 || d.Mood[0] != "melancholic" {
		t.Errorf("Mood = %v, want only melancholic", d.Mood)
	}
}

func TestDossierUnmatchableGenreFallsBackToOther(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["vaporwave","darksynth"],
		"mood":[],"themes":[],"subject_summary":"x","artist_facts":[],
		"notable_line":"","sources":["musicbrainz"],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.StationTags) != 1 || d.StationTags[0] != FallbackStationTag {
		t.Errorf("StationTags = %v, want [%s]", d.StationTags, FallbackStationTag)
	}
	for _, tag := range d.StationTags {
		if tag == "vaporwave" || tag == "darksynth" {
			t.Errorf("a raw out-of-vocabulary value was stored: %v", d.StationTags)
		}
	}
}

func TestDossierDuplicateArrayValuesAreDeduped(t *testing.T) {
	// GBNF enforces set membership but NOT uniqueness; the live probe returned
	// exactly this.
	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["synthwave","synthwave"],
		"mood":["nocturnal","nocturnal"],"themes":[],"subject_summary":"x","artist_facts":[],
		"notable_line":"","sources":["musicbrainz"],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.StationTags) != 1 {
		t.Errorf("StationTags = %v, want one entry", d.StationTags)
	}
	if len(d.Mood) != 1 {
		t.Errorf("Mood = %v, want one entry", d.Mood)
	}
}

// TestDossierSchemaEnumRejectsOutOfVocabulary checks the schema actually sent to
// the model, because enforcement at the sampler is what prevents the drift in
// the first place; post-validation is the second line, not the first.
func TestDossierSchemaEnumRejectsOutOfVocabulary(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(goodJSON())}}
	if _, err := GenerateDossier(context.Background(), llm, input()); err != nil {
		t.Fatal(err)
	}
	if len(llm.schemas) == 0 || llm.schemas[0] == nil {
		t.Fatal("no json_schema was sent to the model")
	}

	raw, err := json.Marshal(llm.schemas[0])
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)

	for _, want := range []string{`"enum"`, `"synthwave"`, `"melancholic"`, `"high"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema does not contain %s:\n%s", want, schema)
		}
	}
	for _, banned := range []string{"vaporwave", "darksynth", "night_drive"} {
		if strings.Contains(schema, banned) {
			t.Errorf("schema contains out-of-vocabulary value %q", banned)
		}
	}
}

// TestDossierNonEnglishSourceYieldsEnglishSummary: the library holds OPM and
// other non-English material, and a Tagalog subject_summary is a dossier the
// break writer cannot use and that costs a re-enrichment to fix.
func TestDossierNonEnglishSourceYieldsEnglishSummary(t *testing.T) {
	in := input()
	in.Artist = "Eraserheads"
	in.Title = "Ang Huling El Bimbo"

	llm := &fakeLLM{replies: []Completion{ok(goodJSON())}}
	if _, err := GenerateDossier(context.Background(), llm, in); err != nil {
		t.Fatal(err)
	}

	prompt := strings.ToLower(llm.prompts[0])
	if !strings.Contains(prompt, "english") {
		t.Errorf("the prompt does not instruct English output:\n%s", llm.prompts[0])
	}
	if !strings.Contains(llm.prompts[0], "Eraserheads") {
		t.Error("the prompt does not carry the track metadata")
	}
}

func TestDossierPromptStatesTheVocabularies(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(goodJSON())}}
	if _, err := GenerateDossier(context.Background(), llm, input()); err != nil {
		t.Fatal(err)
	}
	p := llm.prompts[0]
	for _, want := range []string{"synthwave", "melancholic", FallbackStationTag} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not state the vocabulary value %q", want)
		}
	}
}

func TestDossierUnavailableLLM(t *testing.T) {
	llm := &fakeLLM{errs: []error{errors.New("connection refused")}}

	_, err := GenerateDossier(context.Background(), llm, input())
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("err = %v, want ErrLLMUnavailable", err)
	}
}

func TestDossierNotableLineIsShort(t *testing.T) {
	long := strings.Repeat("a whole verse of copyrighted lyric text ", 20)
	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["synthwave"],"mood":[],"themes":[],
		"subject_summary":"x","artist_facts":[],"notable_line":"` + long + `",
		"sources":["musicbrainz"],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.NotableLine) > MaxNotableLine {
		t.Errorf("notable_line is %d characters, want at most %d: it is a fragment, not a lyric sheet",
			len(d.NotableLine), MaxNotableLine)
	}
}

// --- llama.cpp client ---

func TestLlamaCPPPostsToNativeCompletion(t *testing.T) {
	var path, contentType string
	var body llamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, contentType = r.URL.Path, r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"content": goodJSON(), "stop_type": "eos", "tokens_predicted": 120,
		})
	}))
	defer srv.Close()

	got, err := NewLlamaCPP(srv.URL, srv.Client()).Complete(context.Background(), CompletionRequest{
		Prompt: "catalogue this", JSONSchema: DossierSchema(), NPredict: 400,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// The native endpoint, never the chat route: /v1/chat/completions has three
	// separate failure modes verified by probe.
	if path != "/completion" {
		t.Errorf("posted to %q, want /completion", path)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if body.JSONSchema == nil {
		t.Error("no json_schema was sent; the grammar is what forces valid output")
	}
	if body.NPredict != 400 {
		t.Errorf("n_predict = %d, want 400", body.NPredict)
	}
	if got.StopType != "eos" || got.TokensPredicted != 120 {
		t.Errorf("response = %+v", got)
	}
}

func TestLlamaCPPUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewLlamaCPP(srv.URL, srv.Client()).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Errorf("err = %v, want ErrLLMUnavailable", err)
	}
}

func TestLlamaCPPHealthUsesHealthNotApiTags(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
	}))
	defer srv.Close()

	if err := NewLlamaCPP(srv.URL, srv.Client()).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if path != "/health" {
		t.Errorf("health checked %q, want /health: llama-server has no /api/tags", path)
	}
}

// TestDossierQuotedLyricsAreStripped makes "lyrics are read then discarded" a
// guarantee rather than a hope. A live probe showed the model copying supplied
// lyrics almost verbatim into subject_summary, and subject_summary IS stored
// permanently, so the transient text would have become a permanent copy of
// copyrighted material.
func TestDossierQuotedLyricsAreStripped(t *testing.T) {
	lyrics := "I left the harbour lights behind me\nthe tide was running out\n" +
		"my brother said he would write me\nbut the letters never came"

	in := input()
	in.Lyrics = lyrics

	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["rock"],"mood":["wistful"],
		"themes":["I left the harbour lights behind me the tide"],
		"subject_summary":"I left the harbour lights behind me, the tide was running out.",
		"artist_facts":["The band formed in 1980."],"notable_line":"harbour lights",
		"sources":["lrclib"],"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, in)
	if err != nil {
		t.Fatal(err)
	}

	if d.SubjectSummary != "" {
		t.Errorf("a subject_summary quoting the lyrics was stored: %q", d.SubjectSummary)
	}
	for _, th := range d.Themes {
		if strings.Contains(strings.ToLower(th), "harbour lights behind me the tide") {
			t.Errorf("a theme quoting the lyrics was stored: %q", th)
		}
	}
	// A short fragment is still allowed: notable_line is one hook by design.
	if d.NotableLine != "harbour lights" {
		t.Errorf("NotableLine = %q, want the short fragment kept", d.NotableLine)
	}
	// And a genuine description survives untouched.
	if len(d.ArtistFacts) != 1 {
		t.Errorf("ArtistFacts was damaged: %v", d.ArtistFacts)
	}
}

func TestDossierOriginalSummarySurvives(t *testing.T) {
	in := input()
	in.Lyrics = "I left the harbour lights behind me\nthe tide was running out"

	llm := &fakeLLM{replies: []Completion{ok(`{"station_tags":["rock"],"mood":["wistful"],
		"themes":["leaving","regret"],
		"subject_summary":"A narrator describes departing a coastal town and the family ties left unresolved.",
		"artist_facts":["The band formed in 1980."],"notable_line":"","sources":["lrclib"],
		"confidence":"high"}`)}}

	d, err := GenerateDossier(context.Background(), llm, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.SubjectSummary == "" {
		t.Error("an original summary was stripped; only quotations should be")
	}
	if len(d.Themes) != 2 {
		t.Errorf("Themes = %v, want both kept", d.Themes)
	}
}

func TestDossierNoLyricsMeansNoStripping(t *testing.T) {
	llm := &fakeLLM{replies: []Completion{ok(goodJSON())}}
	d, err := GenerateDossier(context.Background(), llm, input()) // no Lyrics
	if err != nil {
		t.Fatal(err)
	}
	if d.SubjectSummary == "" {
		t.Error("stripping ran with no lyrics supplied")
	}
}
