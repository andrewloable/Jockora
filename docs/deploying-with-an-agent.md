<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Deploying Jockora — an agent playbook

**Audience: an LLM agent with shell access to the target host.** A person can
follow it too, but it is written to be executed rather than read: every step has
a command, an expected result, and what to do when the result differs.

**Ask before you deploy.** Steps 1–4 read the host and decide nothing the
operator cannot undo, so run them freely. Steps 5–8 change what the box runs:
get an explicit yes first, and again for every REDEPLOY. On a server that is
already on air a redeploy restarts the stream and cuts off whoever is listening,
which is the operator's call to make and not yours to infer from the fact that
you have a fix ready. Finish the work, report it, say plainly that it is not
deployed, and wait.

Follow it in order. Steps 1–4 gather facts and make decisions; 5–8 deploy; 9
verifies. **Do not skip step 9.** Every failure listed in step 10 was met on a
real deployment, and most of them look like success until something else breaks
hours later.

Numbers marked *(measured)* come from one real host — a Ryzen 5 5600GT, 15 GB
RAM, Quadro P400 2 GB, 7,696-track library, deployed 2026-09-07. They are one
data point, not a specification. Re-measure on yours; step 7 says how.

---

## 1. Gather the facts before deciding anything

Run all of this first and keep the output. Later steps branch on it.

```sh
# Host
nproc; free -g | head -2; df -h / | tail -1
uname -srm; . /etc/os-release && echo "$PRETTY_NAME"

# Container runtime
docker --version && docker compose version

# GPU — the single most consequential fact for step 3
nvidia-smi --query-gpu=name,memory.total,memory.free --format=csv,noheader 2>/dev/null \
  || echo "NO NVIDIA GPU"

# What the box already runs, so nothing gets clobbered and a free port is picked
docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
ss -ltn | awk '{print $4}' | grep -oE '[0-9]+$' | sort -un | tr '\n' ' '

# The three things Jockora needs supplied from outside
ls -d /path/to/music                       # the library
find / -maxdepth 5 -name '*.gguf' 2>/dev/null | head    # a language model
find / -maxdepth 5 -name 'kokoro*.onnx' -o -name 'voices-v1.0.bin' 2>/dev/null | head
```

**Stop and ask the operator if any of these is missing**: a music folder, a GGUF
model, the two Kokoro files. Jockora degrades rather than failing — no model
means no DJ, no voice means silence where speech should be — so a deployment
missing them *starts fine* and disappoints later. That is worth a question now.

> `docker ps -a` truncated to the first screen is how a running instance gets
> missed and a port collision becomes a mystery. Count them: `docker ps -aq | wc -l`.

---

## 2. Decide where things run

Jockora is one container. Two things stay **outside** it:

| Component | Where | Why |
|---|---|---|
| `llama-server` | host, or its own container | Operator-supplied. Jockora attaches over HTTP and never owns its lifecycle. |
| `ffmpeg` | inside the image | GPL, run out-of-process, never linked. Already in the image. |
| Kokoro weights | host, mounted read-only | 337 MB of someone else's artefacts; a licensing question nobody needs to answer to run a radio station. |
| The music library | host, mounted **read-only** | Jockora must never be able to write to it. Enforce it at the mount, not by trust. |

If `llama-server` runs on the host and Jockora in a container, bind it to the
**docker bridge gateway**, not `0.0.0.0`:

```sh
docker network inspect <network> --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}'
# e.g. 172.20.0.1 -> --host 172.20.0.1
```

That is reachable from the container and not from the LAN. `127.0.0.1` is *not*
reachable from the container; `0.0.0.0` publishes an unauthenticated model server
to the network.

---

## 3. Size the language model — the GPU decision

This is where most of the performance is won or lost, and where the obvious
answer is often wrong.

### 3a. Is there a usable GPU at all?

```sh
nvidia-smi --query-gpu=name,memory.total,memory.free --format=csv,noheader
```

No output, or no NVIDIA driver → **CPU only**, `-ngl 0`. Skip to 3d.

### 3b. Does the whole model fit?

```sh
ls -l model.gguf          # bytes on disk ≈ weights in memory
```

Budget, in VRAM:

```
model file  +  KV cache  +  compute buffer  <  memory.total
```

The KV cache and compute buffer scale with `-c` (context). *(measured)* at
`-c 8192` on a 4 B model the non-weight overhead was **~1.0 GB**; at `-c 4096`,
roughly half.

- **Whole model fits with ~500 MB spare** → offload everything: `-ngl 99`. This
  is the case worth having; expect several times the CPU generation rate.
- **It does not fit** → read 3c before assuming a partial offload helps.

### 3c. Partial offload is usually NOT worth it — check the bandwidth first

Generation speed on a split model is bounded by whichever side is slower. A GPU
only helps if its **memory bandwidth beats the host's**.

```sh
# GPU bandwidth (GB/s), roughly
nvidia-smi --query-gpu=name --format=csv,noheader
# then look it up; or reason from the class of card

# Host bandwidth, roughly: channels x speed x 8 bytes
sudo lshw -short -C memory 2>/dev/null | head
sudo dmidecode -t memory 2>/dev/null | grep -E 'Speed|Type:' | head
```

Rule of thumb:

| GPU memory bandwidth vs host RAM | Do |
|---|---|
| Comfortably higher (most discrete cards ≥ GTX 1060, all RTX) | Offload as many layers as fit |
| Comparable or lower (entry cards: P400, GT 1030, T400, MX-series) | **`-ngl 0`.** Do not split. |

> *(measured, and it cost an hour to learn)* A Quadro P400 (Pascal, ~32 GB/s)
> against dual-channel DDR4 (~50 GB/s): generation was **4.68 tok/s at `-ngl 0`
> and 4.6 tok/s at `-ngl 14`** — indistinguishable. The split bought nothing and
> left 16 MiB of VRAM free, which is an OOM waiting for a busy moment. Reverted.
>
> The lesson generalises: **a small GPU is not a fast GPU.** Measure before you
> keep a tuning change, and revert the ones that do not pay.

If you do offload partially, compute the layer count rather than guessing:

```
layers_to_offload ≈ (VRAM_total − kv_and_compute) / (model_bytes / n_layer)
```

`n_layer` is in the load log (`print_info: n_layer = 32`).

### 3d. Size the context to the actual prompt

Dossier prompts measure **~520 tokens**, generation ~130 *(measured)*. `-c 4096`
is ample and leaves headroom for tracks with long lyrics. `-c 8192` is not
"safer" — it spends about a gigabyte of VRAM on a window nothing uses, VRAM that
could hold layers.

### 3e. Threads

`-t` ≈ physical cores, not threads. On a 6-core/12-thread part, `-t 10` was
fine *(measured)*; beyond physical-core count the returns are small and it
competes with ffmpeg, which the station needs.

### 3f. Make it a service, not a background job

`nohup ... &` over SSH **will die** when the session ends — verified the hard
way. Use systemd, so it also survives a reboot:

```ini
# /etc/systemd/system/llama-server.service
[Unit]
Description=llama-server for Jockora
After=network-online.target docker.service
Wants=network-online.target

[Service]
User=<user>
ExecStart=/path/to/llama-server -m /path/to/model.gguf \
  --host <bridge-gateway> --port 8081 -c 4096 -t <cores> --parallel 1 -ngl <N>
Restart=always
RestartSec=5
StartLimitBurst=0

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload && sudo systemctl enable --now llama-server
systemctl is-active llama-server
curl -s http://<gateway>:8081/health          # {"status":"ok"}
```

`--parallel 1` is deliberate. Jockora enriches serially by design; extra slots
divide the context without adding throughput.

---

## 4. Choose a port and check what is already there

Pick a free port from step 1. If an older Jockora is running, **look at it
before replacing it**:

```sh
docker inspect jockora --format '{{.Config.Image}}{{println}}{{range .Config.Env}}{{println .}}{{end}}'
docker inspect jockora --format '{{json .Mounts}}'
```

**Back up its config directory and database before anything else.** Schema
migrations are one-way:

```sh
STAMP=$(date +%Y%m%d-%H%M%S)
mkdir -p ~/jockora-backup-$STAMP && sudo cp -a <config-dir>/. ~/jockora-backup-$STAMP/
```

---

## 5. Build the image

Build **on the target host** unless you are set up for cross-architecture
builds — an arm64 workstation cannot produce an x86_64 image without buildx.

```sh
rsync -az --delete \
  --exclude '.git/' --exclude 'web/app/node_modules/' --exclude 'web/dist/*' \
  --exclude '*.db' --exclude 'segments/' --exclude 'models/' \
  ./ user@host:/path/to/src/

ssh user@host 'cd /path/to/src && docker build -t jockora:v0.2 .'
```

The Dockerfile is multi-stage and self-contained: a Node stage builds the
browser app, a Go stage embeds it, and the runtime stage is Debian with ffmpeg
and the Python speech sidecar. It needs network access for npm, Go modules and
pip.

**Sanity check the result** — the binary should be materially larger than the Go
code alone, because the browser bundle is embedded in it:

```sh
docker run --rm --entrypoint sh jockora:v0.2 -c 'ls -l /usr/local/bin/jockora'
# ~12-13 MB. Much smaller means the web stage did not run.
```

---

## 6. Compose

```yaml
services:
  jockora:
    image: jockora:v0.2
    container_name: jockora
    restart: always
    networks: [server-network]
    ports:
      - "<free-port>:8080"
    environment:
      JOCKORA_LISTEN_ADDR: "0.0.0.0:8080"
      # REQUIRED in a container. The binary refuses a non-loopback bind without
      # it: the stream and /now.json are readable by anyone who reaches the port.
      JOCKORA_ALLOW_LAN: "1"
      JOCKORA_LIBRARY_PATH: "/music"
      JOCKORA_SEGMENT_DIR: "/tmp/segments"
      JOCKORA_DB_PATH: "/config/jockora.db"
      JOCKORA_LLM_URL: "http://<bridge-gateway>:8081"
      JOCKORA_KOKORO_MODEL: "/models/kokoro-v1.0.onnx"
      JOCKORA_KOKORO_VOICES: "/models/voices-v1.0.bin"
    volumes:
      - /path/to/music:/music:ro        # READ-ONLY. Never a competing writer.
      - /path/to/config:/config
      - /path/to/kokoro:/models:ro
    tmpfs:
      - /tmp/segments:size=512m         # rewritten constantly; keep off the SSD
    command: ["serve"]                  # ENTRYPOINT is the binary; this is required

networks:
  server-network:
    external: true
```

**The config directory must be owned by uid 10001**, the unprivileged user the
image runs as. This is the most common first-run failure and it cannot be fixed
from inside the container:

```sh
sudo chown -R 10001:10001 /path/to/config
```

---

## 7. First run

```sh
docker compose up -d
docker logs -f jockora
```

Expect, in order: a preflight report, a library scan, then `on air`. The scan
probes every file with ffprobe — *(measured)* **~7,700 files in ~9 minutes**, so
a large library is minutes, not seconds. It is not hung.

The line that matters:

```
"msg":"on air", "tracks":7595, "breaks":true, "enriching":true
```

- `breaks:false` → **there is no DJ.** Search the log for `no DJ:` — it says
  which of persona, model or sidecar is missing, and each is a valid degraded
  state, so nothing else will complain.
- `enriching:false` → no dossiers will ever be built, and without dossiers
  **no genre or mood station can be non-empty** (see step 10).

Create exactly one admin. There is no self-registration anywhere in the product,
so until this runs nobody can sign in:

```sh
echo '<passphrase>' | docker compose exec -T jockora jockora admin create -name <name>
```

The password is read from stdin when it is a pipe and from the terminal without
echo otherwise — never from a flag or an environment variable, either of which
would leave it in shell history and in `ps`.

---

## 8. Enrichment: the thing that gates everything else

**Genre and mood both come from dossiers, not from file tags.** A freshly
scanned library has zero dossiers, so on day one:

- a station on genre `other` matches everything unplaced — it works;
- a station on any real genre matches nothing;
- a station on genre **and mood** matches nothing, and stays that way until
  enrichment has covered enough tracks.

Budget for it:

```
hours ≈ tracks × seconds_per_track / 3600
```

*(measured)* **~36 s/track** on a CPU-bound 4 B model — 7,696 tracks ≈ **77
hours**. Enrichment is resumable and cached forever, so this is a background
cost paid once, but it means **a genre+mood station is not available for the
first several hours**. Roughly 300–500 dossiers is usually enough for one
combination to clear the 10-track minimum.

Watch it:

```sh
curl -s -b cookie -c cookie http://host:port/admin/overview.json | jq '.library, .enrichment_cost'
```

If `seconds_per_track` is far worse than the numbers here, check the
`llama-server` timings before touching anything else — they say whether prompt
processing or generation is the cost:

```sh
journalctl -u llama-server -n 200 --no-pager | grep -E 'prompt eval time|eval time'
```

- **prompt eval slow** → context or prompt size; reduce `-c`, or use a smaller model.
- **generation slow** → memory bandwidth. A bigger `-ngl` only helps if step 3c
  says the GPU is actually faster than the host. Otherwise use a smaller model.

---

## 9. Verify — do not skip this

Serving the page is not the same as the app working. Fetch the **bundle the
shell asks for**: a page that loads and then 404s its own script renders nothing
and looks like a blank screen with no error.

```sh
BASE=http://host:port
curl -s $BASE/ | head -c 120                     # <!doctype html> ... <app-root>
S=$(curl -s $BASE/ | grep -oE 'src="[^"]*\.js"' | head -1 | sed 's/src="//;s/"//')
curl -s -o /dev/null -w 'bundle %{http_code} %{size_download}\n' "$BASE/$S"   # 200, ~370 kB
curl -s -o /dev/null -w 'admin %{http_code}\n' $BASE/admin                    # 200

for p in /me /stations.json /admin/users /admin/overview.json; do
  printf '%-22s %s\n' $p "$(curl -s -o /dev/null -w '%{http_code}' $BASE$p)"  # all 401
done
```

Then walk the operator loop with the admin cookie — create a station, assign a
jock, enable it, create a listener, tune, and pull a segment:

```sh
ffprobe -v error -show_entries stream=codec_name,channels,sample_rate seg.ts
ffmpeg -v error -i seg.ts -f null -        # must decode cleanly
```

**Delete anything you created.** If the deployment is being handed to someone
for a first-use test, they must meet a fresh install.

---

## 10. Failures seen in the wild

Each of these was met on a real deployment. All of them look like something else.

| Symptom | Cause | Fix |
|---|---|---|
| Exits immediately, prints usage | `ENTRYPOINT` is the binary; no subcommand | `command: ["serve"]` |
| `listen address is not loopback` and exits | Container binds `0.0.0.0`; the binary refuses without opt-in | `JOCKORA_ALLOW_LAN=1` |
| `attempt to write a readonly database` | Config dir not owned by uid 10001 | `sudo chown -R 10001:10001 <config>` |
| `serve needs something to play` | No library configured. A DB-managed source is **not** enough to start | Set `JOCKORA_LIBRARY_PATH` |
| Port open, connection closes, no page | Something optional is blocking startup — the DJ and enricher are built *before* the HTTP listener | Read the log; look for a component that took its whole start timeout |
| Blank page, no error in the server log | The shell was served and its bundle 404'd | Step 9's bundle fetch |
| Every genre station is empty | No dossiers yet | Step 8. Wait, or use genre `other` |
| Genre+mood station empty, genre alone full | The model left `mood` empty — it is told to, when nothing fits | Wait for more dossiers, or drop the mood |
| `nohup`'d model server vanishes | SSH session ended | systemd (step 3f) |
| Music library gains files | A mount was not `:ro` | Fix the mount. This is an invariant, not a preference |

One more, for anyone automating this: **a readiness loop must report failure.**

```sh
# WRONG — prints a time whether or not it ever came up
for i in $(seq 1 60); do curl -sf $URL && break; sleep 1; done; echo "up!"

# RIGHT
up=no; for i in $(seq 1 60); do curl -sf -o /dev/null $URL && { up=yes; break; }; sleep 1; done
[ $up = yes ] || { echo "NEVER CAME UP"; docker logs jockora | tail -20; exit 1; }
```

The wrong version reported a healthy deployment twice during the work that
produced this document, while the server was still three minutes from listening.

---

## 11. Done looks like

- `docker ps` shows the container `Up`, published on the chosen port.
- The log's last line is `on air`, with `breaks:true` if a DJ was wanted.
- `systemctl is-active llama-server` is `active`, and `is-enabled` is `enabled`.
- Step 9 passes end to end, including a segment that decodes.
- Exactly one admin account exists; no leftover test stations or users.
- `enriched` in `/admin/overview.json` is climbing.
- The operator has been told: the URL, the admin credential, and **how many
  hours until genre and mood stations become usable**.
