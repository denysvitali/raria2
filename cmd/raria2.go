package main

import (
	"github.com/alexflint/go-arg"
	raria2 "github.com/denysvitali/raria2/pkg"
	"github.com/sirupsen/logrus"
	"net/url"
)

var args struct {
	Output  string `arg:"-o" help:"Output directory"`
	DryRun  bool   `arg:"-d" help:"Dry Run" default:"false"`
	Url     string `arg:"positional" help:"The URL from where to fetch the resources from"`
	Workers int    `arg:"-w" help:"Number of workers" default:"5"`
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

	if args.Workers < 1 {
		logrus.Fatalf("invalid number of workers: %d", args.Workers)
	}
	client.ParallelJobs = args.Workers

	err = client.Run()
	if err != nil {
		logrus.Fatal(err)
	}
}
