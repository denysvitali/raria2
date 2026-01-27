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
Usage: raria2 [--output OUTPUT] [--dry-run] [--max-connection-per-server CONNECTIONS] [--max-concurrent-downloads DOWNLOADS] URL [-- ARIA2_OPTS...]

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
  --help, -h             display this help and exit
```


## Example

```
# dry run mirroring into host/path structure automatically
raria2 -d 'https://proof.ovh.net/files/' -- --max-download-limit=1M

# explicitly setting output directory and concurrency knobs
raria2 -d -o output -x 10 -j 8 'https://mirror.nforce.com/pub/speedtests/' -- --max-download-limit=1M
```
