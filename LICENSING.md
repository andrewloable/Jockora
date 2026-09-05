# Licensing

Jockora is **dual-licensed**. Choose whichever track fits what you are doing.

## Open source — AGPL-3.0

The default, and what ships in this repository. See [LICENSE](LICENSE).

You may use, modify, and self-host Jockora freely, **including commercially**, provided you
comply with the AGPL. The obligation that matters: anyone you distribute it to — **or serve
it to over a network** — must be able to obtain your modified source.

If you are running Jockora for yourself, your household, or inside your own organisation
without modifying it, this costs you nothing and requires nothing.

## Commercial license

You need one if you want to do either of these **without publishing your changes**:

- distribute a modified Jockora (a NAS package, an appliance, a bundled product), or
- offer Jockora, modified, as a hosted or managed service.

A commercial license is an exception from the AGPL's source-disclosure obligation.

To arrange one, open an issue on the project repository titled **"Commercial license"**:

<https://github.com/andrewloable/Jockora/issues>

If you would rather not discuss terms in public, say so in the issue and a private channel
will be arranged. The copyright holder is Andrew Loable.

## Contributions

**Jockora does not accept external code contributions.** Dual licensing requires a single
copyright holder — exceptions cannot be sold for code that is not owned outright — and a
CLA is more friction than this project wants.

Everything else is welcome, because none of it is derivative code:

- Bug reports and feature discussion
- **JockPacks** — DJ personas, prompts, catchphrases
- **Voice profiles** and audio processing chains
- **StationPacks** — station presets and configuration
- Documentation

## Clients and the SDK

Clients talk to the Jockora server over HTTP and are **separate works** — the AGPL does not
reach them. Build a client under any license you like. The SDK itself is MIT/Apache-2.0.

## Third-party dependencies

Because the commercial track requires Jockora to sublicense everything it ships, Jockora
takes **permissive dependencies only** (MIT / Apache-2.0 / BSD) inside the binary.

Copyleft tools it uses — ffmpeg, Essentia — run **out-of-process** and are obtained by the
operator, so they are never combined into the distributed work.
