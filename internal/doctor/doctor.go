// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package doctor fails at startup with a precise message instead of at airtime
// with silence.
//
// Three operator-supplied components and a macOS-to-Linux deploy path make this
// the difference between "it works" and an hour of confusion. Every failure
// names the binary, the filter, the URL or the path, and carries the actual
// command to run.
package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MinFreeBytes is the disk headroom below which a long run will eventually die.
const MinFreeBytes = 1 << 30 // 1 GiB

// Check is one preflight result.
type Check struct {
	Name string
	OK   bool
	// Detail says what was actually found.
	Detail string
	// Fix is the command or action that resolves it. Never empty on a failure:
	// "dependency error" sends an operator nowhere.
	Fix string
	// Hard marks a check the server must not start without.
	Hard bool
}

// Config is what doctor needs to know.
type Config struct {
	LibraryPath string
	SegmentDir  string
	DBPath      string
	LLMBaseURL  string
	TTSAddr     string
	// TTSPython and TTSScript describe the sidecar Jockora starts ITSELF.
	//
	// When these are set the address check does not apply: the sidecar is
	// spawned on a port chosen at start-up, so nothing is listening anywhere
	// predictable and probing a fixed address always fails. What matters
	// instead is whether the interpreter exists and can import kokoro-onnx,
	// which is the precondition that actually goes wrong.
	TTSPython  string
	TTSScript  string
	FFmpegPath string
	HTTPClient *http.Client

	// RequireLLM makes the llama-server check a HARD failure.
	//
	// It is off for a run that never calls a model -- the splice spike plays
	// tracks and a pre-rendered WAV and needs no LLM at all -- and on once the
	// DJ brain is wired, where a missing model means no breaks are ever written.
	RequireLLM bool
	// RequireTTS makes the sidecar check hard. Off by default: speech is
	// optional, music is not.
	RequireTTS bool
	// RequireLibrary makes a configured, readable library path mandatory. Off
	// for a run given track files directly, on for a normal scan-and-serve.
	RequireLibrary bool
}

// Run performs every check and returns them in a stable order.
func Run(ctx context.Context, cfg Config) []Check {
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}

	return []Check{
		checkBinary(cfg.FFmpegPath, "ffmpeg"),
		checkBinary(ffprobeBeside(cfg.FFmpegPath), "ffprobe"),
		checkFFmpegFilters(ctx, cfg.FFmpegPath),
		checkLLM(ctx, cfg),
		checkTTS(ctx, cfg),
		checkLibrary(cfg.LibraryPath, cfg.RequireLibrary),
		checkSegmentDir(cfg.SegmentDir),
		checkDBWritable(cfg.DBPath),
		checkDisk(cfg.SegmentDir),
	}
}

// Failed returns the hard checks that did not pass.
func Failed(checks []Check) []Check {
	var out []Check
	for _, c := range checks {
		if !c.OK && c.Hard {
			out = append(out, c)
		}
	}
	return out
}

// Report renders the checks for a terminal.
func Report(checks []Check) string {
	var b strings.Builder
	b.WriteString("JOCKORA PREFLIGHT\n")
	for _, c := range checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %-22s %s\n", mark, c.Name, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(&b, "         fix: %s\n", c.Fix)
		}
	}
	return b.String()
}

func ffprobeBeside(ffmpegPath string) string {
	dir, base := filepath.Split(ffmpegPath)
	if base == "ffmpeg" {
		return filepath.Join(dir, "ffprobe")
	}
	return "ffprobe"
}

func checkBinary(path, name string) Check {
	c := Check{Name: name, Hard: true}
	found, err := exec.LookPath(path)
	if err != nil {
		c.Detail = fmt.Sprintf("%s not found on PATH", path)
		c.Fix = "install it: apt install ffmpeg   (or: brew install ffmpeg)"
		return c
	}
	c.OK, c.Detail = true, found
	return c
}

// checkFFmpegFilters verifies the filters the mixer actually uses.
//
// A present ffmpeg WITHOUT loudnorm fails much later and much more confusingly,
// during a library scan rather than at startup.
func checkFFmpegFilters(ctx context.Context, ffmpegPath string) Check {
	c := Check{Name: "ffmpeg filters", Hard: true}

	out, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-filters").Output()
	if err != nil {
		c.Detail = fmt.Sprintf("could not list filters: %v", err)
		c.Fix = "check that " + ffmpegPath + " runs: " + ffmpegPath + " -filters"
		return c
	}

	var missing []string
	for _, f := range []string{"loudnorm", "aresample", "afade", "volume"} {
		if !bytes.Contains(out, []byte(" "+f+" ")) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		c.Detail = "missing filter(s): " + strings.Join(missing, ", ")
		c.Fix = "this ffmpeg was built without them; install a full build (apt install ffmpeg, or brew install ffmpeg)"
		return c
	}
	c.OK, c.Detail = true, "loudnorm, aresample, afade, volume present"
	return c
}

// checkLLM does a live json_schema round-trip, not a reachability probe.
//
// A llama-server that is UP but ignores json_schema fails at dossier time rather
// than at startup, which is exactly the class of late failure this package
// exists to prevent.
func checkLLM(ctx context.Context, cfg Config) Check {
	c := Check{Name: "llama-server", Hard: cfg.RequireLLM}
	if cfg.LLMBaseURL == "" {
		c.Detail = "no LLM URL configured"
		c.Fix = "set --llm-url or JOCKORA_LLM_URL"
		return c
	}

	body, _ := json.Marshal(map[string]any{
		"prompt": "ok",
		"json_schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
			"required":   []any{"ok"},
		},
		// 16 was too tight and produced a FALSE FAILURE: the model emitted
		// whitespace-formatted JSON, ran out of budget mid-object, and the
		// truncated text did not parse -- which this check then reported as
		// "json_schema was ignored", telling an operator to upgrade llama.cpp
		// over a working server. 64 is ample for {"ok":true} in any formatting.
		"n_predict": 64,
	})

	url := strings.TrimSuffix(cfg.LLMBaseURL, "/") + "/completion"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		c.Detail = fmt.Sprintf("%s unreachable: %v", url, err)
		c.Fix = "llama-server is not running. Start it: llama-server -m <model.gguf> --host 127.0.0.1 --port 8080"
		return c
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.Detail = url + " returned 404"
		c.Fix = "endpoint not found; this may be an OpenAI-only server. Jockora needs llama.cpp's native /completion."
		return c
	}
	if resp.StatusCode != http.StatusOK {
		c.Detail = fmt.Sprintf("%s returned %s", url, resp.Status)
		c.Fix = "check the llama-server log; a model may not be loaded"
		return c
	}

	var reply struct {
		Content  string `json:"content"`
		StopType string `json:"stop_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		c.Detail = "reply was not JSON: " + err.Error()
		c.Fix = "server reachable but did not answer as llama.cpp does; check what is listening on " + cfg.LLMBaseURL
		return c
	}

	var shaped map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(reply.Content)), &shaped); err != nil {
		// Truncation and a disregarded schema look identical in the text and
		// have completely different fixes, so they are distinguished here
		// rather than guessed at by whoever reads the report.
		if reply.StopType == "limit" {
			c.Detail = fmt.Sprintf("the probe ran out of token budget; got %.60q", reply.Content)
			c.Fix = "this is a doctor bug, not a server fault: raise n_predict in checkLLM"
			return c
		}
		c.Detail = fmt.Sprintf("json_schema was ignored; got %.60q", reply.Content)
		c.Fix = "server reachable but json_schema was ignored; upgrade llama.cpp."
		return c
	}
	if _, ok := shaped["ok"]; !ok {
		c.Detail = fmt.Sprintf("schema not honoured; required key missing from %.60q", reply.Content)
		c.Fix = "server reachable but json_schema was ignored; upgrade llama.cpp."
		return c
	}

	c.OK, c.Detail = true, url+" honoured a json_schema round-trip"
	return c
}

// checkTTS is soft: speech is optional, music is not. A missing sidecar costs
// breaks, not the stream.
func checkTTS(ctx context.Context, cfg Config) Check {
	c := Check{Name: "tts sidecar", Hard: cfg.RequireTTS}

	// A managed sidecar is checked by whether it COULD start, not by whether
	// it is already running. Probing a fixed address here reported a healthy
	// managed sidecar as unreachable and refused to start the server.
	if cfg.TTSPython != "" {
		return checkTTSInterpreter(ctx, cfg, c)
	}

	if cfg.TTSAddr == "" {
		c.Detail = "no TTS URL configured"
		c.Fix = "set --tts-url or JOCKORA_TTS_URL; without it the DJ never speaks"
		return c
	}

	url := strings.TrimSuffix(cfg.TTSAddr, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		c.Detail = fmt.Sprintf("%s unreachable: %v", url, err)
		c.Fix = "start the Kokoro sidecar; without it every break is dropped and only music plays"
		return c
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Detail = fmt.Sprintf("%s returned %s", url, resp.Status)
		c.Fix = "check the sidecar log"
		return c
	}
	c.OK, c.Detail = true, url+" responding"
	return c
}

func checkLibrary(path string, required bool) Check {
	c := Check{Name: "library path", Hard: required}
	if path == "" {
		if !required {
			// A run given track files directly has no library, and that is not
			// a fault.
			c.OK, c.Detail = true, "not configured (tracks supplied directly)"
			return c
		}
		c.Detail = "no library path configured"
		c.Fix = "set --library-path or JOCKORA_LIBRARY_PATH"
		return c
	}
	fi, err := os.Stat(path)
	if err != nil {
		c.Detail = err.Error()
		c.Fix = "check the path exists and is readable: ls " + path
		return c
	}
	if !fi.IsDir() {
		c.Detail = path + " is not a directory"
		c.Fix = "point --library-path at the folder holding your music"
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		c.Detail = "not readable: " + err.Error()
		c.Fix = "fix permissions: chmod +rx " + path
		return c
	}
	f.Close()
	c.OK, c.Detail = true, path
	return c
}

func checkSegmentDir(dir string) Check {
	c := Check{Name: "segment dir", Hard: true}
	if dir == "" {
		c.Detail = "no segment directory configured"
		c.Fix = "set --segment-dir or JOCKORA_SEGMENT_DIR"
		return c
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.Detail = "cannot create " + dir + ": " + err.Error()
		c.Fix = "create it or choose a writable location: mkdir -p " + dir
		return c
	}
	probe := filepath.Join(dir, ".jockora-write-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		c.Detail = dir + " is not writable: " + err.Error()
		c.Fix = notWritableFix(dir)
		return c
	}
	os.Remove(probe)
	c.OK, c.Detail = true, dir+" writable"
	return c
}

// notWritableFix names the fix that actually works, which is usually not the
// one people try first.
//
// In a container this is the FIRST thing an operator hits: the image runs
// unprivileged as uid 10001, a bind-mounted config directory belongs to whoever
// created it on the host, and no amount of chmod inside the container changes
// that. "chmod +w" was the old advice and it is wrong for this case -- the
// ownership has to be fixed on the HOST, by uid, before the container starts.
func notWritableFix(dir string) string {
	if u, err := user.Current(); err == nil && u.Uid != "0" {
		return fmt.Sprintf("this process runs as uid %s; on the HOST that owns %s run: "+
			"chown %s %s   (or chmod a+w %s)", u.Uid, dir, u.Uid, dir, dir)
	}
	return "fix permissions on " + dir + ": chmod a+w " + dir
}

func checkDBWritable(dbPath string) Check {
	c := Check{Name: "database", Hard: true}
	if dbPath == "" {
		c.Detail = "no database path configured"
		c.Fix = "set --db-path or JOCKORA_DB_PATH"
		return c
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.Detail = "cannot create " + dir + ": " + err.Error()
		c.Fix = "mkdir -p " + dir
		return c
	}
	probe := filepath.Join(dir, ".jockora-db-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		c.Detail = dir + " is not writable: " + err.Error()
		c.Fix = notWritableFix(dir)
		return c
	}
	os.Remove(probe)
	c.OK, c.Detail = true, dbPath
	return c
}

// checkDisk is soft: a full disk kills a long run, but refusing to start is
// worse than warning and letting the operator watch it.
func checkDisk(dir string) Check {
	c := Check{Name: "free disk"}
	if dir == "" {
		dir = "."
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		c.Detail = "could not measure: " + err.Error()
		return c
	}
	free := uint64(st.Bsize) * st.Bavail
	c.Detail = fmt.Sprintf("%.1f GiB free", float64(free)/float64(1<<30))
	if free < MinFreeBytes {
		c.Fix = "free space under " + dir + "; segments accumulate and a long run will stop"
		return c
	}
	c.OK = true
	return c
}

// checkTTSInterpreter asks the sidecar's own python whether it can do the job.
//
// This catches the failure that actually happens -- kokoro-onnx installed into
// the wrong interpreter, or not at all -- at startup, instead of at the first
// break, where it looks like a DJ that has nothing to say.
func checkTTSInterpreter(ctx context.Context, cfg Config, c Check) Check {
	if _, err := os.Stat(cfg.TTSScript); err != nil {
		c.Detail = fmt.Sprintf("sidecar script %s: %v", cfg.TTSScript, err)
		c.Fix = "set --tts-script to sidecar/kokoro_server.py"
		return c
	}

	probe, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probe, cfg.TTSPython, "-c", "import kokoro_onnx").CombinedOutput()
	if err != nil {
		c.Detail = fmt.Sprintf("%s cannot import kokoro_onnx: %v", cfg.TTSPython, strings.TrimSpace(string(out)))
		c.Fix = "python3.10 -m venv .venv-tts && .venv-tts/bin/pip install kokoro-onnx, then --tts-python .venv-tts/bin/python"
		return c
	}

	// THE WEIGHTS, not just the library. Importing kokoro_onnx proves nothing
	// about whether there is a model to load: with the weights missing the
	// sidecar starts, fails, and the supervisor waits out its FULL start
	// timeout -- three minutes of apparent success -- before the station goes
	// on air silently with no DJ. That degrades correctly and reads like a
	// hang, so it is caught here instead, where an operator is already looking.
	for _, m := range []struct{ what, path string }{
		{"model", kokoroPath("JOCKORA_KOKORO_MODEL", "models/kokoro-v1.0.onnx")},
		{"voices", kokoroPath("JOCKORA_KOKORO_VOICES", "models/voices-v1.0.bin")},
	} {
		if _, err := os.Stat(m.path); err != nil {
			c.Detail = fmt.Sprintf("kokoro %s file %s: %v", m.what, m.path, err)
			c.Fix = "download kokoro-v1.0.onnx and voices-v1.0.bin, then set " +
				"JOCKORA_KOKORO_MODEL and JOCKORA_KOKORO_VOICES to point at them"
			return c
		}
	}

	c.OK, c.Detail = true, cfg.TTSPython+" can import kokoro_onnx, and the voice models are present"
	return c
}

// kokoroPath resolves where the sidecar will look for a weights file. The
// sidecar reads these env vars itself and falls back to a path relative to its
// working directory, so the check has to resolve them the same way or it
// verifies a file the sidecar will never open.
func kokoroPath(env, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return fallback
}
