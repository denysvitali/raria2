# raria2

A wrapper for [aria2](https://aria2.github.io/) to mirror open directories.

This CLI tool tries to emulate the same behavior of `wget --recursive`, but with a couple of filters,
checks and caching and by using aria2c to perform the download of resources.

## Compile

```
go build .
```

## Usage

```
Usage: raria2 [--output OUTPUT] [--dry-run] [--max-connection-per-server CONNECTIONS] [--max-concurrent-downloads DOWNLOADS] [--aria2-session-size SIZE] [--max-depth DEPTH] [--accept EXT] [--reject EXT] [--accept-filename GLOB] [--reject-filename GLOB] [--case-insensitive-paths] [--accept-path PATTERN] [--reject-path PATTERN] [--visited-cache FILE] [--write-batch FILE] [--http-timeout DURATION] [--user-agent UA] [--rate-limit RATE] URL [-- ARIA2_OPTS...]

Positional arguments:
  URL                    The URL from where to fetch the resources from
  ARIA2_OPTS             Options forwarded to aria2c after the URL (use -- before them if
                         they look like flags)

Options:
  --output OUTPUT, -o OUTPUT
                         Output directory. If omitted, raria2 mirrors into
                         <host>/<path>/ derived from the URL (similar to wget)
  --dry-run, -d          Dry Run [default: false]
  --max-connection-per-server, -x
                         Parallel connections per download [default: 5]
  --max-concurrent-downloads, -j
                         Maximum concurrent downloads [default: 5]
  --aria2-session-size SIZE
                          Number of links to feed a single aria2 process before
                          closing stdin and restarting it. 0 keeps a single
                          session. See "Aria2 stdin bug workaround" below for details.
                          [default: 0]
  --max-depth DEPTH      Maximum HTML depth to crawl (-1 for unlimited) [default: -1]
  --accept EXT           Comma-separated list(s) of extensions to include (no dot, case-insensitive)
  --reject EXT           Comma-separated list(s) of extensions to exclude
  --accept-filename GLOB  Comma-separated list(s) of filename globs to include
  --reject-filename GLOB  Comma-separated list(s) of filename globs to exclude
  --case-insensitive-paths
                         Make path matching case-insensitive
  --accept-path PATTERN  Path glob (default) or regex:<expr> that must match to crawl/download
  --reject-path PATTERN  Path glob or regex to skip
  --visited-cache FILE   Persist visited URLs to this file so interrupted runs can resume
  --write-batch FILE     Write aria2 input file to disk instead of executing
  --http-timeout DURATION
                         HTTP client timeout as Go duration string (e.g. 30s, 2m) [default: 30s]
  --user-agent UA         Custom User-Agent string [default: raria2/1.0]
  --rate-limit RATE       Rate limit for HTTP requests (requests per second) [default: 0]
  --respect-robots        Respect robots.txt when crawling [default: false]
  --accept-mime TYPES     Comma-separated list of MIME types to include
  --reject-mime TYPES     Comma-separated list of MIME types to exclude
  --help, -h             display this help and exit
```


## Example

```
# dry run mirroring into host/path structure automatically
raria2 -d 'https://proof.ovh.net/files/' -- --max-download-limit=1M

# explicitly setting output directory and concurrency knobs
raria2 -d -o output -x 10 -j 8 'https://mirror.nforce.com/pub/speedtests/' -- --max-download-limit=1M

# customize the HTTP timeout (here: 2 minutes)
raria2 --http-timeout=2m 'https://example.com/pub/'

# limit crawl depth to first directory level
raria2 --max-depth=1 'https://example.com/pub/'

# only download .iso files inside /iso/ paths and persist visited cache
raria2 --accept=iso --accept-path='glob:/iso/**' --visited-cache=visited.txt 'https://mirror.example.com/'

# Advanced filtering: filename globs and case-insensitive paths
raria2 --accept-filename='release-*' --reject-filename='*.tmp' --case-insensitive-paths --accept-path='glob:**/Files/**' 'https://example.com/pub/'

# Generate batch file for later use instead of immediate download
raria2 --write-batch downloads.txt --dry-run 'https://example.com/pub/'
# Later use: aria2c --input-file=downloads.txt

# Rate-limited crawling with custom User-Agent
raria2 --rate-limit=2 --user-agent='MyBot/1.0' 'https://example.com/pub/'

# Respect robots.txt while crawling
raria2 --respect-robots 'https://example.com/pub/'

# MIME-type filtering - only download PDFs and images
raria2 --accept-mime 'application/pdf,image/jpeg,image/png' 'https://example.com/files/'

# Exclude binary executables and archives
raria2 --reject-mime 'application/octet-stream,application/x-executable,application/zip' 'https://example.com/pub/'
```

## Aria2 stdin bug workaround

Aria2 has an open bug ([aria2/aria2#1161](https://github.com/aria2/aria2/issues/1161))
where downloads fed via stdin might not start until the input stream closes.
When crawling large trees, use `--aria2-session-size` to periodically close and
restart aria2 so downloads begin before the crawl finishes. This option only
applies when streaming URLs directly to aria2 (normal mode), not when
`--write-batch` is used.

## Session Management

The `--visited-cache` option allows you to persist visited URLs between runs, enabling resume functionality:

```bash
# First run - crawl and cache visited URLs
raria2 --visited-cache=visited.txt 'https://example.com/pub/'

# Interrupted run - resume from where you left off
raria2 --visited-cache=visited.txt 'https://example.com/pub/'
```

## Batch File Generation

Use `--write-batch` to create an aria2 input file for manual control. If you
hit the aria2 stdin bug (aria2/aria2#1161) on large crawls, combine
`--aria2-session-size` with `--write-batch` to generate smaller chunks:

```bash
# Create batch file without downloading
raria2 --write-batch downloads.txt 'https://example.com/pub/'

# Use the batch file later with aria2
aria2c --input-file=downloads.txt
```
