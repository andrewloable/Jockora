<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Installing and running Jockora

Two ways in: the compose file, or by hand on a host. Both were run on a clean
directory before being written down, and the output quoted is what they actually
printed.

Moved here out of the README so that file can stay about what Jockora *is*.
See also [configuration.md](configuration.md) for every setting,
[hardware.md](hardware.md) for whether you need a GPU, and
[deploying-with-an-agent.md](deploying-with-an-agent.md) for the checks that
catch a deployment which looks fine and is not.

## Quickstart

You need Docker, a music folder, and a GGUF language model. Everything else is
in the compose file.

**1. Configure.** Compose reads `.env` automatically, so you set the paths once:

```sh
cp .env.example .env
$EDITOR .env          # MUSIC, CONFIG, MODELS, MODEL
```

**2. Fetch the speech models** into `<CONFIG>/models` (337 MB; operator-supplied
and deliberately not vendored, like ffmpeg):

```sh
mkdir -p config/models && cd config/models
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin
cd ../..
```

**3. Give the config directory to uid 10001.** The container runs unprivileged
and cannot chown a bind mount from inside. This is the most common first-run
failure:

```sh
sudo chown -R 10001 ./config
```

**4. Start it:**

```sh
docker compose up -d
```

The compose file builds the `jockora` image from this checkout (`build: .`) —
portable, no registry dependency. Every tagged release also publishes prebuilt
images to Docker Hub and GHCR, so you can pull instead of building:

```sh
docker pull andrewloable/jockora:latest    # or ghcr.io/andrewloable/jockora:latest
```

To use one, swap `build: .` for `image: andrewloable/jockora:latest` under the
`jockora` service in `docker-compose.yml`.

**5. Make yourself an account.** Nobody can sign in until you do, and there is
no self-registration:

```sh
docker compose exec jockora jockora admin create -name you
```

The password is read from the terminal without echo — not from a flag or an
environment variable, either of which would leave it in shell history and in a
`ps` listing.

Open **http://localhost:8080**, sign in, and pick a station: the dial is the
whole listener UI. **http://localhost:8080/admin** is the operator console —
sources, stations, playlists, jocks and accounts — and only an admin account can
open it.

`jockora doctor` runs at startup and refuses to serve on a failed check, so a
broken setup names the failing check rather than going quiet. `docker compose
logs -f jockora` to watch it.

Compose starts `llama-server` alongside Jockora and waits for it to report
healthy first. Already run your own model server? Delete the `llm` service and
point `JOCKORA_LLM_URL` at it.

### The settings that matter

| Variable | What it does |
|---|---|
| `MUSIC` | Your library, mounted **read-only** |
| `CONFIG` | Database and voice models. Must be owned by uid 10001 |
| `MODELS` / `MODEL` | Directory holding your `.gguf`, and its filename |

Inside the container everything is a `JOCKORA_`-prefixed environment variable
with a matching flag. Every one is documented in
[configuration.md](configuration.md), which also carries a
troubleshooting guide organised around the failure you will actually see —
usually "the music plays and the DJ never says anything", which has four
different causes and four different fixes.

Handing the install to a coding agent instead?
[deploying-with-an-agent.md](deploying-with-an-agent.md) is written to
be executed rather than read: how to size the language model against the GPU you
actually have (and when a GPU is worth nothing at all), what to verify before
believing a deployment worked, and the ten failures that look like something
else.

## Install on a host, without containers

The same program, assembled by hand. Every command below was run on a clean
directory before it was written down; the output quoted is what it actually
printed.

**1. Build it.** The browser app first: `web/dist` is build output and the
binary embeds it, so skipping this compiles, links, starts, and serves an
"assets not built" notice to every listener.

```sh
cd web/app && npm ci && npx ng build && cd ../..
CGO_ENABLED=0 go build -o jockora ./cmd/jockora
```

**2. Install ffmpeg, and check the two filters.** Operator-supplied and run out
of process, never linked — that is a licensing requirement, not a preference.
Both filters are used on every break:

```sh
ffmpeg -hide_banner -filters | grep -E ' (loudnorm|aresample) '
```

**3. Make the speech sidecar's virtualenv.** Python 3.10; kokoro-onnx pulls
onnxruntime with it.

```sh
python3.10 -m venv tts
./tts/bin/pip install kokoro-onnx
./tts/bin/python -c "import kokoro_onnx"
```

**4. Fetch the voice models** (337 MB, operator-supplied for the same reason
ffmpeg is):

```sh
mkdir -p models && cd models
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin
cd ..
```

**5. Start a language model.** Either `llama-server` from llama.cpp with a GGUF
on disk, or a hosted OpenAI-compatible endpoint — see **Hosted endpoints** in
[configuration.md](configuration.md). Note the port: Jockora looks at
**8081**, because it is already using 8080 itself.

```sh
llama-server -m /path/to/model.gguf --host 127.0.0.1 --port 8081
```

**6. Point Jockora at all of it.** Every flag has an environment variable:
`JOCKORA_` plus the flag name uppercased, dashes to underscores, so
`-library-path` is `JOCKORA_LIBRARY_PATH`.

```sh
export JOCKORA_LIBRARY_PATH=/path/to/music
export JOCKORA_DB_PATH=$PWD/jockora.db
export JOCKORA_PERSONA=personas/
export JOCKORA_TTS_PYTHON=$PWD/tts/bin/python
export JOCKORA_KOKORO_MODEL=$PWD/models/kokoro-v1.0.onnx
export JOCKORA_KOKORO_VOICES=$PWD/models/voices-v1.0.bin
```

**7. Check it before starting it.** `jockora doctor` names what is missing and
what to do about it, and `serve` refuses to start on a failed check:

```
$ ./jockora doctor
JOCKORA PREFLIGHT
  [ok  ] ffmpeg                 /opt/homebrew/bin/ffmpeg
  [ok  ] ffprobe                /opt/homebrew/bin/ffprobe
  [ok  ] ffmpeg filters         loudnorm, aresample, afade, volume present
  [ok  ] llama-server           http://127.0.0.1:8081/completion honoured a json_schema round-trip
  [ok  ] tts sidecar            can import kokoro_onnx, and the voice models are present
  [ok  ] library path           /path/to/music
  [ok  ] segment dir            segments writable
  [ok  ] database               jockora.db
  [ok  ] free disk              139.0 GiB free
```

**8. Make yourself an account, then start.** Nobody can sign in until you do,
and there is no self-registration:

```sh
./jockora admin create -name you     # asks for a password, twice, without echo
./jockora serve
```

`admin create` writes to `JOCKORA_DB_PATH`, so **export it rather than putting
it in front of a pipe** — a variable set on the left of a `|` belongs to the
left-hand command, and the account lands in a different database than the one
the server reads. The account is created, the message says so, and sign-in then
fails with 401. It cost half an hour to find; it takes one `export` to avoid.

Open **http://127.0.0.1:8080**, sign in, and pick a station.
