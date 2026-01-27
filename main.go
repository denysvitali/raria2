package main

import (
	"net/url"

	"github.com/alexflint/go-arg"
	raria2 "github.com/denysvitali/raria2/pkg"
	"github.com/sirupsen/logrus"
)

var args struct {
	Output                 string   `arg:"-o" help:"Output directory (defaults to host/path derived from the URL)"`
	DryRun                 bool     `arg:"-d,--dry-run" help:"Dry Run" default:"false"`
	Url                    string   `arg:"positional" help:"The URL from where to fetch the resources from"`
	MaxConnectionPerServer int      `arg:"-x,--max-connection-per-server" help:"Parallel connections per download" default:"5"`
	MaxConcurrentDownload  int      `arg:"-j,--max-concurrent-downloads" help:"Maximum concurrent downloads" default:"5"`
	Aria2Args              []string `arg:"positional" help:"Options forwarded to aria2c after the URL (use -- before them if they look like flags)"`
}

func main() {
	arg.MustParse(&args)

	if args.Url == "" {
		logrus.Fatal("please provide an URL")
	}

	parsedUrl, err := url.Parse(args.Url)
	if err != nil {
		logrus.Fatalf("invalid URL provided")
	}

	client := raria2.New(parsedUrl)
	client.OutputPath = args.Output
	client.Aria2AfterURLArgs = args.Aria2Args

	if args.MaxConnectionPerServer < 1 {
		logrus.Fatalf("invalid value for --max-connection-per-server: %d", args.MaxConnectionPerServer)
	}
	client.MaxConnectionPerServer = args.MaxConnectionPerServer

	if args.MaxConcurrentDownload < 1 {
		logrus.Fatalf("invalid value for --max-concurrent-downloads: %d", args.MaxConcurrentDownload)
	}
	client.MaxConcurrentDownload = args.MaxConcurrentDownload

	err = client.Run()
	if err != nil {
		logrus.Fatal(err)
	}
}
