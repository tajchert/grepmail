# grepmail — developer notes

This is a working note for engineers picking the project up. The user-facing
docs live in `README.md`; this file is about the *why* behind the code.

## What we're building

A single-binary CLI that searches large mbox archives. Performance target:
header-only queries on a 1 GB mbox should answer in well under a second on
a warm cache; with the sidecar index, the same queries answer in
milliseconds regardless of file size (we're iterating a slice, not parsing
mail). Body-search queries on the indexed path are mmapped and fanned
out across all available cores; literal `--body` patterns skip the regex
engine entirely.

## Code map

```
cmd/grepmail/main.go     # tiny shim, calls into internal/cli
internal/cli/            # subcommand dispatch + flag binding + the
                          # search runner that picks streaming vs indexed
internal/mbox/           # streaming mbox parser
internal/filter/         # filter.Spec — predicates, header- vs body-phase
internal/index/          # sidecar index (gob format, magic-checked)
internal/output/         # writers: summary/json/mbox/raw/count/ids
```

## The mbox format

We follow the RFC 4155 / mboxrd description with real-world tolerance:

- A message starts with a line beginning with `From ` (capital F, space).
- That line is the *envelope* line, not part of RFC 5322 headers. We strip
  it for `--format raw` and keep it for `--format mbox`.
- The boundary between messages is `\n\nFrom ` — a `From ` line at the
  start of a line that is preceded by a blank line. We don't trust
  unblanked `From ` lines mid-body even though strict mbox forbids them,
  because Gmail Takeout does occasionally leak them.
- Headers and body are separated by a blank line (`\n\n` or `\r\n\r\n`).

## How parsing works

`internal/mbox/parser.go` is a single-pass `Scanner` over a `bufio.Reader`.
For each message we parse the header block (cheap — net/mail does this) and
record the absolute byte offset of the leading `From ` line plus the total
length up to the next message boundary. The body is *not* read until a
caller asks for it via `ReadRaw` / `ReadBody` — those use `ReadAt` so they
work even on a stream that's already been advanced.

The scanner peeks one line ahead at message boundaries: when we see a
`From ` line that follows a blank, we stash it as `pendingFromLine` and
finish the current message. The next `Next()` call consumes the stashed
line as the start of the next message. That's how we avoid double-reading
or buffering whole messages.

Header decoding for display (Subject, attachment filenames, etc.) goes
through `mime.WordDecoder`. We deliberately keep the From/To headers raw
in the index so a user's `--from` regex sees what's actually in the file.

## The filter engine

`filter.Spec` separates predicates into two phases:

1. `MatchHeaders(*HeaderView)` — header/date/Message-ID filters.
2. `MatchBody(body, header, raw)` — body regex, attachment heuristics.

`Spec.NeedsBody()` reports whether the second phase is required. The
search runner uses this to skip body reads entirely when nothing in the
query depends on them — that's where most of the speed comes from for
header-only queries on the indexed path.

The header view is a lightweight struct populated either from a parsed
`*mbox.Message` or from an `index.Entry`. That dual source is the entire
abstraction needed to share the filter engine between the streaming and
indexed runners.

### Literal `--body` fast-path

When the user's `--body` pattern has no regex metacharacters
(`regexp.QuoteMeta(pat) == pat`), `flags.build` populates
`Spec.BodyLiteral` (and `BodyLiteralFold` when `-i` is set) alongside
the compiled `BodyRe`. `MatchBody` prefers the literal path
(`bytes.Contains` or `indexFold`) over the regex engine. On arm64 the
NEON-tuned `bytes.IndexByte` makes this dramatically faster — measured
~50× on case-insensitive literal queries against a 700 MB mbox.
Patterns with metacharacters still flow through `*regexp.Regexp`
unchanged.

The `indexFold` / `containsFold` helpers (also used by the attachment
heuristics) do ASCII case-insensitive substring search without
allocating a lowercased copy of the haystack.

## The sidecar index

The index is a gob-encoded `index.File` with a magic-byte prefix
(`grepmail-idx\x01`). It stores per-message:

- `Offset`, `Length`, `HeaderEnd` — random access into the mbox.
- Decoded From/To/Cc/Subject/Message-ID/Date/MIMEType for header filtering
  without touching the mbox.

Staleness is detected by comparing `SourceSize` and `SourceMod` against
`os.Stat` of the mbox. If they differ, `Load` returns `ErrStale` and the
runner falls back to streaming. We do not auto-rebuild — that would be
surprising behaviour for a CLI tool. Users explicitly run `grepmail index`.

The index format is intentionally simple. If we ever need to evolve it,
bump the `Version` field and let `Load` reject old versions; users will
just re-index.

## The runner

`internal/cli/run.go::runSearch` is the dispatcher:

1. If `--no-index` is set or no index is present, stream the mbox.
2. Otherwise call `runIndexed`, which:
   - Skips opening the mbox entirely when the query is header-only — the
     whole answer comes from `idx.Entries`.
   - When body or raw bytes are needed, memory-maps the mbox via
     `mbox.OpenMapped` (syscall.Mmap on unix, `*os.File` fallback
     elsewhere). `MappedFile.Slice(off, len)` returns a zero-copy view
     into the mapping; this replaces the per-message
     `make([]byte, e.Length) + ReadAt` allocations the original code did.
   - For body-needing queries, fans entries out across a worker pool
     sized to `runtime.GOMAXPROCS(0)` (`runIndexedParallel`). Output
     order is preserved by enqueuing a per-job result channel onto a
     queue; the drain reads them in submission order. `--limit` and
     writer errors signal an early stop via a once-closed `stop`
     channel; in-flight workers drain naturally.
   - Header-only queries stay serial (`runIndexedSerial`) — per-entry
     work is too cheap for worker dispatch to pay back.

`evalEntry` is the shared per-entry pipeline used by both serial and
parallel paths.

Two important properties this gives us:

- Pure-header queries on an indexed mbox never read or mmap the file.
- Adding a header filter to a body-search query effectively prunes the
  candidate set before any body I/O happens, because `MatchBody` only runs
  when `MatchHeaders` accepts. That's why combinations like
  `--from foo --body bar` are dramatically faster than `--body bar` alone.

### Memory-mapped reads

`internal/mbox/mmap_unix.go` (`//go:build unix`) wraps `syscall.Mmap`;
`mmap_other.go` is a `*os.File`-backed fallback with the same API. Both
expose `Slice(off,len) []byte` and satisfy `io.ReaderAt`. Slices are
valid until `Close`; writers in `internal/output` consume them
synchronously per `Write` call so this is safe.

## Style and conventions

- The codebase has no third-party runtime dependencies — only stdlib. This
  is deliberate: it keeps the homebrew formula trivial and the binary
  small. Don't pull in cobra/viper/etc. without a strong reason.
- Errors at the CLI boundary go to stderr with a `grepmail:` prefix; the
  exit code is `1` for runtime failure, `2` for usage/flag errors, `0`
  for success.
- The mbox file is opened for reading only. The tool never writes back to
  it. Index writes are atomic via temp-file + rename.
- We treat the sample.mbox in this repo as a test fixture but it's
  gitignored at the repo level (it's hundreds of MB of personal data).

## Adding a new filter

1. Add a field to `filter.Spec`.
2. Wire it in `MatchHeaders` or `MatchBody` (and `NeedsBody` if applicable).
3. Add the corresponding flag in `internal/cli/flags.go::register` and
   `build`.
4. If the filter benefits from index data, add it to `index.Entry` and
   bump the index `Version`.
5. Document in `README.md`.

## Adding a new output format

Implement the `output.Writer` interface and add a case to `output.New`.
The interface is intentionally minimal — `Write(Hit)` is called once per
match in arrival order; `Close()` flushes anything buffered (e.g. counts).

## Testing locally

There's a `sample.mbox` in the working directory used during development.
Don't commit it — it's gitignored. To smoke-test a build:

```sh
go build -o grepmail ./cmd/grepmail
./grepmail stats sample.mbox
./grepmail index sample.mbox
./grepmail count --from example.com --since 2025-01-01 sample.mbox
```

## Release / homebrew

The Homebrew tap (`tajchert/homebrew-tap`) builds grepmail from the
source tarball via `go build` — there are no prebuilt binaries and no
goreleaser/CI wiring (yet). Cutting a release is a three-step manual
flow:

1. **Tag and push:** `git tag -a vX.Y.Z -m "..." && git push origin vX.Y.Z`.
2. **Compute the tarball SHA:**
   ```sh
   curl -sL https://github.com/tajchert/grepmail/archive/refs/tags/vX.Y.Z.tar.gz \
     | shasum -a 256
   ```
3. **Update the tap formula** in `tajchert/homebrew-tap`'s
   `Formula/grepmail.rb` — bump the `url` to the new tag and replace
   `sha256` with the value from step 2. Commit and push the tap repo.
   Mirror the same change in this repo's `Formula/grepmail.rb` so the
   two stay in sync.

Verify with `brew install --build-from-source tajchert/tap/grepmail`
(untap/retap first if a previous version is cached).

There's a `.goreleaser.yaml` checked in but no GitHub Action invokes
it. If brew users start asking for prebuilt binaries, wire up
`goreleaser/goreleaser-action@v6` on `push: tags: ['v*']` with a
`HOMEBREW_TAP_GITHUB_TOKEN` secret and the manual flow above goes
away.
