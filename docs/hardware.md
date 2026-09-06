<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Do I need a GPU?

**No.** A GPU makes the one-time enrichment pass faster. It is not required, and
nothing about running the station day to day needs one.

Every number below was measured on 2026-09-06, not estimated: `llama-bench` on
Qwen2.5-7B-Instruct-Q5_K_M, an 806-token enrichment prompt, and 128 tokens
generated per track averaged over 124 real tracks from a 7,696-track library.

## The only expensive thing is enrichment, and it happens once

Each track gets one LLM pass, cached forever. Re-scanning does not redo it.

| Configuration | Per track | 5,000 tracks |
|---|---|---|
| CPU only, Apple M4 Pro (`-ngl 0`) | 13.9 s | **19 hours** |
| Metal GPU, same machine | 5.3 s | **7 hours** |
| CPU only, typical NUC or NAS (~⅓ of an M4 Pro) | 41.8 s | **58 hours** |
| Hosted endpoint | — | **~$0.60** |

The NUC row is an extrapolation and is labelled as one. The other three are
measurements.

## Why 58 hours is not the problem it looks like

Enrichment runs **in the background while the station plays**. It is not a setup
step you wait on:

- The station goes on air as soon as the library is scanned — minutes, not hours.
- A track with no dossier yet is not a broken track. The DJ talks from
  personality alone and asserts nothing, which is a designed state, not a
  degraded one.
- The queue is resumable. Stop the server, start it next week, and it continues
  from where it stopped.

So the honest framing is not "58 hours before you can listen". It is "the DJ
knows more about your library each evening for the first few days".

## Choosing

**CPU-only llama.cpp — the default, and the recommendation.** Free, private,
and nothing leaves the machine. Point `-llm-url` at a `llama-server` you run, or
give `-llm-model <file.gguf>` and Jockora starts and supervises one itself.

**A hosted endpoint — reasonable, with one caveat that matters here.**
`-llm-api openai` speaks to OpenRouter, Groq, Together or a local vLLM;
`-llm-api ollama` speaks to Ollama. At roughly $0.60 for 5,000 tracks the cost
is not the deciding factor. **The deciding factor is that your library's artist
and title metadata leaves the box**, one request per track. For an audience that
self-hosts Navidrome or Jellyfin specifically to avoid that, it is a real
tradeoff and not a footnote. Some hosted models are free at the time of writing,
which changes the price and none of the privacy.

**A smaller model is the better lever than a bigger machine.** These figures are
for a 7B at Q5. A 3B at Q4 is roughly three times faster and, in this project's
own evaluation, the model size mattered far less than whether the chat template
was applied at all.

## What actually needs a GPU: nothing, today

Speech runs on CPU. Kokoro synthesises a break faster than real time on an
ordinary machine, and the lookahead buffer gives 7–17 minutes of slack besides.

The design reserves "GPU models run serially" for a future in which a larger
speech model shares one card with the writer. That is not the current
configuration and does not constrain a CPU-only deployment.
