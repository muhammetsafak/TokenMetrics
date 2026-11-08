# CSV format

TokenMetrics can import usage from a CSV file (for services that leave no
logs) and export your data in the same format. This page describes the format.
For a lossless copy of your data (for example to move it to another computer),
use the JSON export instead; see [JSON export](#json-export).

## Columns

The first line must be a header. Column order is free, names are
case-insensitive, and the delimiter may be `,` or `;` (detected from the
header line). A UTF-8 byte order mark is accepted. Unknown or repeated column
names are rejected, so a typo cannot silently drop data.

```
timestamp,service,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,project,session,cost_usd,total_only_tokens
```

| Column               | Required | Meaning |
|----------------------|----------|---------|
| `timestamp`          | yes      | When the usage happened. See [Timestamps](#timestamps). |
| `service`            | yes      | Free text, for example `anthropic`, `openai`, `my-internal-api`. |
| `model`              | yes      | Model name. |
| `input_tokens`       | no       | Whole number, `0` or more. Empty means `0`. |
| `output_tokens`      | no       | As above. |
| `cache_read_tokens`  | no       | As above. |
| `cache_write_tokens` | no       | As above. Stored as 5-minute cache writes. |
| `project`            | no       | Free text. |
| `session`            | no       | Free text. |
| `cost_usd`           | no       | Cost reported by the source, in USD, `0` or more, `.` as decimal point. Empty means "not reported", which is different from `0`. |
| `total_only_tokens`  | no       | Whole number, `0` or more. For a source that reports only a combined token count with no input/output split (for example newer Codex CLI logs). See [Total-only rows](#total-only-rows). Empty means `0`. |

A row needs `timestamp`, `service`, `model`, and at least one token column
above zero. The three required columns must exist in the header; the other
columns may be left out entirely. Files written before `total_only_tokens`
existed (the same header without that last column) stay valid, and importing
one again after an upgrade still adds nothing.

### Total-only rows

A row with `total_only_tokens` above zero is stored as a *total-only* record:
the count is kept as is, it is part of the total token figures, and **no cost
is computed for it**, because a cost needs the input/output split. It is not
counted as input tokens. Such a row must not also have `input_tokens`,
`output_tokens`, `cache_read_tokens` or `cache_write_tokens` above zero; a row
that mixes them is rejected with a reason. A `cost_usd` on the row is still
kept.

Limits: text up to 256 characters, token counts up to 10^15, `cost_usd` up to
10^9 per row, timestamps from 1970 to at most one day in the future, file size
up to 50 MiB.

### Timestamps

RFC 3339 is recommended: `2026-03-01T10:00:00Z` or
`2026-03-01T13:00:00+03:00`. These forms are read as UTC when they carry no
time zone: `2026-03-01T10:00:00`, `2026-03-01 10:00:00`, `2026-03-01`.
Fractions of a second are kept to the millisecond.

## Example

```
timestamp,service,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,project,session,cost_usd,total_only_tokens
2026-03-01T10:00:00Z,anthropic,claude-sonnet,1000,500,200,100,app,s1,0.0123,
2026-03-01T11:30:00+03:00,openai,gpt-5,2000,800,0,0,"my, project",s2,,
2026-03-02,google,gemini-pro,300,,,,,,,
2026-03-03T09:00:00Z,openai,gpt-5.5,,,,,,s3,,1500000
```

Fields containing the delimiter, a double quote or a line break must be quoted
with `"`, and a quote inside a quoted field is doubled (`""`).

## What happens on import

- Valid rows are stored with source `csv_import`; invalid rows are skipped.
  After the import you get a list of the rejected rows with their line number
  (the header is line 1) and the reason. Blank lines are ignored.
- Problems with the file as a whole (empty, larger than 50 MiB, missing
  required column, unknown column) reject the import without storing anything.
- **Importing the same file twice changes nothing.** Each row is identified by
  a SHA-256 hash of its normalized content (trimmed text, timestamp in UTC,
  canonical numbers), so a row that is already stored is recognized, even when
  the file uses another delimiter or time zone notation.
- Consequence: **two completely identical rows count as one.** If you really
  made two identical calls, make the rows differ, for example by a different
  `session` or by a timestamp that differs by a millisecond.
- Changing any value of a row (for example a corrected token count) and
  importing again adds a new record next to the old one; it does not replace
  it. Remove the old data first if you want to correct it.

## What the export writes

CSV export uses the same columns, `,` as delimiter, UTC timestamps with
milliseconds, and the selected date range and filters. It carries less than
the app stores:

- `cache_write_tokens` is the sum of the 5-minute and 1-hour cache writes.
- `total_only_tokens` holds the combined count of a total-only record (its other token columns are `0`) and is `0` for every other record. Files with this column are rejected by app versions that predate it (unknown column); delete the column there.
- Thinking tokens, request counts, source and device are not in the CSV.
- The CSV does not carry the identity of a record (its source and id). An
  imported row always becomes a new `csv_import` record.

**Do not import a CSV export into a database that already holds those records
(for example the one it was exported from).** The import cannot recognize
them, so every row is counted a second time and your totals double. Use the CSV
export to look at or edit data elsewhere, or to import it into an empty
database. To move or merge data between computers use the
[JSON export](#json-export), which keeps record identity and is safe to import
into a database that already has the same records. The import screen of the
app should show this warning before importing.

### Spreadsheet formula protection

A text cell (`service`, `model`, `project`, `session`) that starts with `=`,
`+`, `-`, `@`, a tab or a carriage return would be run as a formula when the
file is opened in Excel or similar programs. The export therefore prefixes such
cells with a single quote (`'`), so `=1+1` becomes `'=1+1`. The import removes
that quote again, so exported data returns unchanged. Do not remove the quote
by hand before opening the file in a spreadsheet.

## JSON export

The JSON export contains every stored field of the selected records, including
each record's `source`, `external_id` and `device_id`, plus a top-level
`schema_version` (currently `1`). Importing it on another computer merges the
records with the existing ones; importing the same file again changes nothing.
A file with an unknown `schema_version` is rejected. Text values are imported
as stored (up to 32768 characters each), and timestamps are not checked against
the clock, because the other computer's clock may differ. Records that already
exist (same `source` and `external_id`) are overwritten with the values from
the file.

Settings, API keys and other secrets are never part of any export.
