package cmd

import (
	"github.com/alexflint/go-arg"
	raria2 "github.com/denysvitali/raria2/pkg"
	"github.com/sirupsen/logrus"
	"net/url"
)

type Arguments struct {
	url string `name:"url" help:"The URL from where to fetch the resources from"`
}

var args Arguments

func main(){
	arg.MustParse(&args)

	if args.url == "" {
		logrus.Fatal("please provide an URL")
	}

	parsedUrl, err := url.Parse(args.url)
	if err != nil {
		logrus.Fatalf("invalid URL provided")
	}

	client := raria2.New(parsedUrl)

	err = client.Run()
	if err != nil {
		logrus.Fatal(err)
	}
}