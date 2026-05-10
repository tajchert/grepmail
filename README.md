# grepmail

Fast, grep-style CLI for searching and exploring `mbox` mail archives.

`grepmail` is built for the case where you have a multi-gigabyte mbox export
(say, a Gmail Takeout) and want to slice it from the terminal: count
messages from a sender, list everything in a date range, dump matching
messages back out as a fresh mbox, or pull a single message by Message-ID.

It streams the mbox by default — no setup required — and an optional
sidecar index makes repeated queries answer in milliseconds.

## Install

### Homebrew

```sh
brew install tajchert/tap/grepmail
```

### From source

```sh
go install github.com/tajchert/grepmail/cmd/grepmail@latest
```

Requires Go 1.22+.

## Quick start

```sh
# How many messages, what date range, who sends the most?
grepmail stats inbox.mbox

# All messages from a given sender since January
grepmail search --from example.com --since 2025-01-01 inbox.mbox

# Anything mentioning "invoice" in the body, last 30 days, JSON output
grepmail search --body invoice -i --since 30d --format json inbox.mbox

# Build a sidecar index for fast repeated queries (optional but recommended
# for large files). The index is auto-invalidated when the mbox changes.
grepmail index inbox.mbox

# Same query as before — now answers in milliseconds.
grepmail count --from example.com --since 2025-01-01 inbox.mbox
```

## Commands

| Command | What it does |
|---|---|
| `grepmail search` | Stream matching messages using filter flags. Default output is a one-line summary. |
| `grepmail list`   | Alias for `search` (summary output). |
| `grepmail count`  | Print only the number of matches. |
| `grepmail show`   | Print one message in full. Select with `--id`, `--n`, or `--offset`. `--headers` / `--body` narrow the output. |
| `grepmail index`  | Build a sidecar index (`<mbox>.grepmail-index`). |
| `grepmail stats`  | High-level summary: count, date range, top senders. |

## Filters

All filter flags compose with AND semantics. Unless noted, `=PATTERN` values
are Go regular expressions; pass `-i` for case-insensitive matching.

| Flag | Description |
|---|---|
| `--from PATTERN` / `--to` / `--cc` | Match the corresponding header. |
| `--subject PATTERN` | Match the RFC 2047-decoded Subject. |
| `--body PATTERN` | Match the message body (forces a body read). |
| `--match PATTERN` / `-e` | Match anywhere in headers or body. |
| `--header-match PATTERN` | Match the raw header block. |
| `--header KEY=PATTERN` | Match a specific header. Repeatable. |
| `--since DATE` / `--until DATE` | Date range. Accepts `2025-01-31`, `2025-01-31 14:00`, RFC 3339, `today`, `yesterday`, or relative offsets like `7d`, `2w`, `3m`, `1y`, `12h`. |
| `--has-attachment true|false` | Include only messages that do/don't have attachments. |
| `--attachment-name PATTERN` | Match attachment filenames. |
| `--mime PATTERN` | Match the top-level Content-Type. |
| `--id ID` | Exact Message-ID match. |
| `--n N` | Match the message at 0-based position N. |
| `-i` | Case-insensitive regex matching. |
| `--limit N` | Stop after N matches. |
| `--no-index` | Skip the sidecar index. |

## Output formats

`grepmail search --format <fmt>`:

- `summary` (default): one line per message — offset, date, from, subject.
- `json`: one JSON object per line (jq-friendly).
- `mbox`: full matching messages in mbox format. Pipe to a file to create a
  new mailbox containing only the matches.
- `raw`: RFC 5322 message bytes, no `From ` envelope. Useful for piping into
  another mail tool.
- `count`: integer count.
- `ids`: one Message-ID per line.

## Performance

- Streaming search reads the file once with a 1 MiB buffered reader. On a
  modern SSD, header-only queries (e.g. `--from`, `--subject`, date ranges)
  run at roughly the disk's sequential read speed.
- With a sidecar index, header-only queries are answered straight from the
  index without opening the mbox at all — typical runs are a few
  milliseconds regardless of file size.
- Body searches (`--body`, `--match`, attachment filters) need to read
  every candidate's bytes. The indexed path memory-maps the mbox so body
  slices are zero-copy, and body matching is fanned out across all
  available cores. Combining body filters with specific header filters is
  the single biggest win — header filters prune candidates before any
  body work happens.
- The sidecar index stores offsets plus pre-decoded headers for every
  message. It's roughly 0.1% of the mbox size and is invalidated
  automatically when the mbox's size or modtime changes.

### How fast is it?

Numbers below are from a 710 MB / 2,474-message Gmail Takeout on a 10-core
Apple Silicon laptop, warm cache, indexed, 5-run averages.

| Query | Time |
|---|---|
| `grepmail count --from noreply` (header-only) | <1 ms |
| `grepmail count --since 2025-01-01` (header-only) | <1 ms |
| `grepmail count --from github --body invoice -i` (header-prefiltered) | <1 ms |
| `grepmail count --body github` (literal) | 70 ms |
| `grepmail count --body invoice -i` (literal, case-insens) | 160 ms |
| `grepmail count --attachment-name '\.pdf$'` | 120 ms |
| `grepmail count --body '(?i)invoice\|receipt\|payment'` (regex) | 8.5 s |

For comparison, the equivalent line-counting `grep` runs over the same
file (which doesn't actually parse messages — it counts matching lines):

| Query | Time | Speedup |
|---|---|---|
| `grep -c github` | 2.5 s | grepmail is **35×** faster |
| `grep -ic invoice` | 8.9 s | grepmail is **54×** faster |
| `grep -Eic 'invoice\|receipt\|payment'` | 21 s | grepmail is **2.5×** faster |

Two things to note:

1. Header-only and header-prefiltered queries don't open the mbox at all
   — they answer straight from the index. Adding a `--from` or `--since`
   filter to a body query usually drops the time to milliseconds.
2. Literal `--body` patterns (the common case) skip the regex engine and
   use `bytes.Contains` directly — that's where the 35–50× wins over grep
   come from. Patterns with regex metacharacters still beat grep, but
   the gap is smaller because both end up doing similar regex work.

## License

MIT.
