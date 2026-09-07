// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package config holds Jockora's runtime configuration. Flags and environment
// variables only: no config file parser, because flags plus env is the smallest
// surface that works identically in a container and as a bare binary.
package config

import (
	"flag"
	"fmt"
	"github.com/andrewloable/jockora/internal/mix"
	"os"
	"strings"
)

const envPrefix = "JOCKORA_"

// Config is the whole of Jockora's runtime configuration.
type Config struct {
	LibraryPath string // music library root; read-only, Jockora never writes here

	// SubsonicURL points at an OpenSubsonic server -- Navidrome, Airsonic,
	// Gonic -- instead of a local folder. The target listener usually already
	// runs one, and pointing at it removes the whole "give it a folder and wait
	// for a scan" step.
	// TUNING KNOBS. Every default here is the value the project was measured
	// with; changing nothing changes nothing. They are applied ONCE at startup,
	// before the mixer exists, because the audio path reads them without
	// synchronisation.
	MusicLUFS        float64 // music loudness target, LUFS
	MusicDuckedLUFS  float64 // music while the DJ speaks
	SpeechLUFS       float64 // speech loudness target
	TruePeakCeiling  float64 // dBTP ceiling held by the limiter
	LookaheadSeconds float64 // how far ahead a break is generated
	AdEveryNBreaks   int     // one break slot in N becomes an advert
	AdIntervalMin    float64 // floor between adverts, minutes
	FactConfidence   float64 // dossier confidence a fact must reach to be assertable

	SubsonicURL      string
	SubsonicUser     string
	SubsonicPassword string
	DBPath           string // SQLite file holding dossiers, stations and state

	// SessionKey signs listener sessions. Empty means one is generated on
	// first run and kept in the database, so a restart does not sign everyone
	// out. Supply it only to share sessions across two servers.
	SessionKey  string
	SegmentDir  string // where HLS segments and the playlist are written
	ListenAddr  string // host:port for the HTTP server; loopback by default
	LLMBaseURL  string // llama-server base URL, native /completion API
	TTSAddr     string // Kokoro sidecar base URL
	PersonaPath string // persona TOML describing the jock
	// TTSPython is the interpreter the speech sidecar runs under.
	//
	// PINNED, not inherited. onnxruntime has no wheels for the system python3
	// on the development host, and relying on whatever python3 resolves to is
	// exactly what made the voice epic look unbuildable. The sidecar is a
	// separate process anyway, so a dedicated 3.10 venv costs nothing.
	TTSPython string
	TTSScript string // the sidecar entry point

	// LLMModelPath, when set, makes Jockora START AND SUPERVISE llama-server
	// itself rather than expecting one to be running.
	//
	// Both modes are first class. An operator who already runs a model server,
	// or runs it on a GPU box, sets -llm-url and Jockora manages nothing. One
	// who wants a single thing to run sets -llm-model. Neither needs a rebuild,
	// and swapping the model is a flag.
	LLMModelPath   string
	LLMBinary      string // llama-server; found on PATH by default
	LLMContextSize int
	LLMGPULayers   int

	// LLMAPI selects the dialect spoken to LLMBaseURL: "llamacpp" (the
	// default) or "ollama".
	//
	// It exists because an operator very often already runs something. The
	// deployment target here has a 2 GB GPU, so the model usually belongs on
	// another machine entirely -- and telling Jockora to use what is already
	// there beats making them run a second server. llama.cpp stays the default
	// so nothing changes for anyone who has not asked for this.
	LLMAPI string

	// LLMAPIKey authenticates a hosted endpoint. Prefer the environment
	// variable: a key on a command line ends up in shell history and in `ps`
	// output for every user on the box.
	LLMAPIKey   string
	StationName string // which station to broadcast

	SampleRate        int // canonical bus sample rate
	Channels          int // canonical bus channel count
	SegmentSeconds    int // HLS target segment duration
	ListSize          int // HLS playlist window, in segments
	BreakEveryNTracks int // schedule a DJ break after this many tracks

	CrossfadeSeconds float64 // default crossfade length at a track boundary

	// LogFormat is "text", "json", or empty to infer from whether stderr is a
	// terminal.
	LogFormat string

	// AllowLAN permits a non-loopback listen address. Off by default: this
	// program speaks plain HTTP, so binding off-host is a decision, never
	// an accident.
	AllowLAN bool

	// Args holds the positional arguments left after flag parsing, so a command
	// can read them without parsing os.Args a second time.
	Args []string
}

// Load reads configuration from the command line, then fills in any flag the
// operator did not set from the matching environment variable. Precedence is
// flag, then env, then default.
func Load() (*Config, error) { return LoadWith(nil) }

// LoadWith is Load, with a chance to register additional flags first.
//
// It exists because there is exactly one flag set: a command that registered
// its own flags on flag.CommandLine would have them rejected here as unknown,
// which is a failure that only appears when the binary is actually run.
func LoadWith(register func(*flag.FlagSet)) (*Config, error) {
	return LoadArgs(os.Args[1:], register)
}

// LoadArgs is LoadWith over an explicit argument list.
//
// It exists so the command surface can be tested without reaching into
// os.Args, which is the difference between asserting that an unknown
// subcommand exits 2 and hoping it does.
func LoadArgs(args []string, register func(*flag.FlagSet)) (*Config, error) {
	var c Config

	fs := flag.NewFlagSet("jockora", flag.ContinueOnError)
	if register != nil {
		register(fs)
	}
	fs.StringVar(&c.LibraryPath, "library-path", "", "music library root (read-only)")
	fs.Float64Var(&c.MusicLUFS, "music-lufs", mix.DefaultMusicLUFS, "music loudness target in LUFS")
	fs.Float64Var(&c.MusicDuckedLUFS, "music-ducked-lufs", mix.DefaultMusicDuckedLUFS, "music loudness while the DJ speaks")
	fs.Float64Var(&c.SpeechLUFS, "speech-lufs", mix.DefaultSpeechLUFS, "speech loudness target in LUFS")
	fs.Float64Var(&c.TruePeakCeiling, "true-peak-ceiling", mix.DefaultTruePeakCeilingDBTP, "true-peak ceiling in dBTP")
	fs.Float64Var(&c.LookaheadSeconds, "lookahead-seconds", 150, "how far ahead of a boundary a break is generated")
	fs.IntVar(&c.AdEveryNBreaks, "ad-every-n-breaks", 4, "one break slot in N becomes an advert (0 disables adverts)")
	fs.Float64Var(&c.AdIntervalMin, "ad-interval-minutes", 90, "minimum minutes between adverts")
	fs.Float64Var(&c.FactConfidence, "fact-confidence", 0.6, "dossier confidence a fact must reach before the DJ may assert it")
	fs.StringVar(&c.SubsonicURL, "subsonic-url", "", "OpenSubsonic server to read the library from instead of a folder (Navidrome, Airsonic, Gonic)")
	fs.StringVar(&c.SubsonicUser, "subsonic-user", "", "OpenSubsonic username")
	fs.StringVar(&c.SubsonicPassword, "subsonic-password", "", "OpenSubsonic password (prefer JOCKORA_SUBSONIC_PASSWORD)")
	fs.StringVar(&c.DBPath, "db-path", "jockora.db", "SQLite database file")
	fs.StringVar(&c.SessionKey, "session-key", "", "secret signing listener sessions, at least 32 bytes (prefer JOCKORA_SESSION_KEY); empty generates one and keeps it in the database")
	fs.StringVar(&c.SegmentDir, "segment-dir", "segments", "directory for HLS segments")
	// Loopback, never 0.0.0.0. Accounts guard the stream and the console now,
	// but there is still no TLS -- so a LAN bind sends every password across
	// the wire in clear, and self-hosters port-forward services without
	// realising.
	fs.StringVar(&c.ListenAddr, "listen-addr", "127.0.0.1:8080", "HTTP listen address")
	// 8081, not llama-server's own default of 8080, because Jockora already
	// listens there. Start llama-server with --port 8081 or set JOCKORA_LLM_URL.
	fs.StringVar(&c.LLMBaseURL, "llm-url", "http://127.0.0.1:8081", "llama-server base URL")
	fs.StringVar(&c.TTSAddr, "tts-url", "http://127.0.0.1:8090", "TTS sidecar base URL")
	fs.StringVar(&c.PersonaPath, "persona", "", "persona TOML for the jock")
	fs.StringVar(&c.TTSPython, "tts-python", "python3.10", "python interpreter for the speech sidecar (needs kokoro-onnx)")
	fs.StringVar(&c.TTSScript, "tts-script", "sidecar/kokoro_server.py", "speech sidecar entry point")
	fs.StringVar(&c.LLMModelPath, "llm-model", "", "GGUF to serve; set this to have Jockora run llama-server itself")
	fs.StringVar(&c.LLMBinary, "llm-binary", "llama-server", "llama-server executable, when -llm-model is set")
	fs.IntVar(&c.LLMContextSize, "llm-context", 8192, "llama-server context size")
	fs.IntVar(&c.LLMGPULayers, "llm-gpu-layers", 99, "layers to offload to the GPU (99 offloads what fits)")
	fs.StringVar(&c.LLMAPI, "llm-api", "llamacpp",
		`API spoken to -llm-url: "llamacpp" (default), "ollama", or "openai" for any OpenAI-compatible host such as OpenRouter`)
	fs.StringVar(&c.LLMAPIKey, "llm-api-key", "", "API key for a hosted endpoint (prefer JOCKORA_LLM_API_KEY)")
	fs.StringVar(&c.StationName, "station", "", "station to broadcast")

	fs.IntVar(&c.SampleRate, "sample-rate", 48000, "canonical bus sample rate")
	fs.IntVar(&c.Channels, "channels", 2, "canonical bus channel count")
	fs.IntVar(&c.SegmentSeconds, "segment-seconds", 4, "HLS segment duration")
	fs.IntVar(&c.ListSize, "list-size", 10, "HLS playlist window, in segments")
	fs.IntVar(&c.BreakEveryNTracks, "break-every-n-tracks", 4, "tracks between DJ breaks")

	fs.Float64Var(&c.CrossfadeSeconds, "crossfade-seconds", 2.0, "crossfade length at a track boundary")

	fs.StringVar(&c.LogFormat, "log-format", "",
		`"text", "json", or empty to infer (json when not a terminal)`)

	fs.BoolVar(&c.AllowLAN, "allow-lan", false,
		"permit a non-loopback listen address (plain HTTP: passwords cross the wire in clear)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || explicit[f.Name] {
			return
		}
		v := os.Getenv(envKey(f.Name))
		if v == "" {
			return
		}
		// Set, not a manual assignment, so ints and floats are parsed and
		// rejected the same way the flag would have been.
		if setErr := fs.Set(f.Name, v); setErr != nil {
			err = fmt.Errorf("%s: %w", envKey(f.Name), setErr)
		}
	})
	if err != nil {
		return nil, err
	}

	c.Args = fs.Args()
	return &c, nil
}

// envKey maps a flag name to its environment variable: listen-addr becomes
// JOCKORA_LISTEN_ADDR.
func envKey(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}
