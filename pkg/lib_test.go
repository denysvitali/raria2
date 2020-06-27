package raria2

import (
	"github.com/stretchr/testify/assert"
	"net/url"
	"testing"
)

func TestSameUrl(t *testing.T) {
	firstUrl, _ := url.Parse("https://example.com/")
	secondUrl, _ := url.Parse("https://example.com/?C=S;O=A")
	thirdUrl, _ := url.Parse("https://example.com/?C=M;O=A")
	fourthUrl, _ := url.Parse("https://example.com/a")
	assert.True(t, SameUrl(firstUrl, secondUrl))
	assert.True(t, SameUrl(firstUrl, thirdUrl))
	assert.True(t, SameUrl(secondUrl, thirdUrl))
	assert.False(t, SameUrl(firstUrl, fourthUrl))
}


func TestRun1(t *testing.T){
	webUrl, err := url.Parse("http://www.ieee802.org/1/files/public/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.dryRun = true

	client.Run()
}

func TestRun2(t *testing.T){
	webUrl, err := url.Parse("http://8.oldhacker.org/txt/More%20Hacking/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.OutputPath = "./output"
	client.dryRun = true

	err = client.Run()
	assert.Nil(t, err)
}

func TestIsHtmlPage(t *testing.T){
	webUrl, err := url.Parse("https://home.cern/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.dryRun = true

	isHtmlPage, err := client.IsHtmlPage(webUrl.String())
	assert.Nil(t, err)
	assert.True(t, isHtmlPage)

	isHtmlPage, err = client.IsHtmlPage("https://www.google.com/images/branding/googlelogo/2x/googlelogo_color_272x92dp.png")
	assert.Nil(t, err)
	assert.False(t, isHtmlPage)
}