# raria2

A wrapper for [aria2](https://aria2.github.io/) to mirror open directories.

This CLI tool tries to emulate the same behavior of `wget --recursive`, but with a couple of filters,
checks and caching and by using aria2c to perform the download of resources.

## Compile

```
go build -o raria2 cmd/raria2.go
```

## Usage 

```
Usage: raria2 [--output OUTPUT] [--dryrun] [--workers WORKERS] URL

Positional arguments:
  URL                    The URL from where to fetch the resources from

Options:
  --output OUTPUT, -o OUTPUT
                         Output directory
  --dryrun, -d           Dry Run [default: false]
  --workers WORKERS, -w WORKERS
                         Number of workers [default: 5]
  --help, -h             display this help and exit
```


## Example

```
raria2 -d -o output 'http://8.oldhacker.org/txt/More%20Hacking/' -w 1
```
