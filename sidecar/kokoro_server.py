# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
"""Kokoro speech sidecar.

Speaks HTTP on JOCKORA_TTS_ADDR. The Go side supervises this process; it does
not supervise the Go side. Two rules matter here:

  * The model loads BEFORE the socket opens. /health answering 200 therefore
    means "warm and ready", never "starting up" -- the Go manager treats a
    healthy sidecar as one it may hand a break to immediately.
  * Nothing here retries. A failure returns 503 and the caller drops the break.

INTERPRETER. Run this under Python 3.10. onnxruntime has no wheels for the
system python3 on the development host, which is what made this look
unbuildable; pinning the interpreter here costs nothing because this is already
a separate process.

    python3.10 -m venv .venv-tts && .venv-tts/bin/pip install kokoro-onnx
"""

import io
import json
import os
import struct
import sys
import urllib.parse
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODEL = os.environ.get("JOCKORA_KOKORO_MODEL", "models/kokoro-v1.0.onnx")
VOICES = os.environ.get("JOCKORA_KOKORO_VOICES", "models/voices-v1.0.bin")
DEFAULT_VOICE = os.environ.get("JOCKORA_KOKORO_VOICE", "am_michael")

_kokoro = None


def load():
    global _kokoro
    from kokoro_onnx import Kokoro  # imported late so --check can report cleanly

    _kokoro = Kokoro(MODEL, VOICES)


def synth(text, voice):
    samples, rate = _kokoro.create(text, voice=voice or DEFAULT_VOICE, speed=1.0, lang="en-us")
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        # float32 -1..1 to 16-bit. Kokoro does not clip, but a stray sample
        # wrapping to full-scale negative would be an audible crack, so clamp.
        w.writeframes(b"".join(
            struct.pack("<h", max(-32768, min(32767, int(s * 32767)))) for s in samples))
    return buf.getvalue()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        # stderr is drained by the Go supervisor and logged at debug; per-request
        # lines there are noise, and a blocked stderr looks like a model hang.
        pass

    def _send(self, code, body, ctype="text/plain"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/health":
            self._send(200, b"ok")
        elif parsed.path == "/onset":
            self._onset(urllib.parse.parse_qs(parsed.query))
        else:
            self._send(404, b"not found")

    def _onset(self, q):
        """Vocal-onset analysis. Same process as synthesis, by design.

        It is a second endpoint rather than a second sidecar because the process
        already exists and a cgo binding was ruled out. Analysis is slow --
        seconds per track -- but it runs during library enrichment, never on the
        break path, and ThreadingHTTPServer keeps /health answering while it
        works so the Go supervisor does not read a long analysis as a death.
        """
        path = (q.get("path") or [""])[0]
        if not path:
            self._send(400, b"path is required")
            return
        try:
            duration = float((q.get("duration") or ["0"])[0])
        except ValueError:
            duration = 0.0
        try:
            import vocal_onset
            body = json.dumps(vocal_onset.analyse(path, duration)).encode()
            self._send(200, body, "application/json")
        except Exception as e:  # noqa: BLE001 - no analysis is a valid answer
            print(f"onset failed for {path}: {e}", file=sys.stderr, flush=True)
            self._send(503, str(e).encode())

    def do_POST(self):
        if self.path != "/synth":
            self._send(404, b"not found")
            return
        try:
            n = int(self.headers.get("Content-Length", 0))
            req = json.loads(self.rfile.read(n) or b"{}")
            text = (req.get("text") or "").strip()
            if not text:
                self._send(400, b"empty text")
                return
            self._send(200, synth(text, req.get("voice")), "audio/wav")
        except Exception as e:  # noqa: BLE001 - any failure is "no speech this time"
            print(f"synth failed: {e}", file=sys.stderr, flush=True)
            self._send(503, str(e).encode())


def main():
    addr = os.environ.get("JOCKORA_TTS_ADDR")
    if not addr:
        sys.exit("JOCKORA_TTS_ADDR is not set; the Go supervisor normally sets it")
    host, _, port = addr.rpartition(":")
    load()
    print(f"kokoro ready on {addr}", file=sys.stderr, flush=True)
    ThreadingHTTPServer((host, int(port)), Handler).serve_forever()


if __name__ == "__main__":
    main()
