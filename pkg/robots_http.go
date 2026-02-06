package raria2

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
)

func (r *RAria2) urlAllowedByRobots(u *url.URL) bool {
	if !r.RespectRobots {
		return true
	}

	robots, err := r.getRobotsData(u.Host)
	if err != nil {
		logrus.Debugf("failed to fetch robots.txt for %s: %v", u.Host, err)
		return true // fail open
	}

	return robots.TestAgent(u.Path, r.UserAgent)
}

func (r *RAria2) getRobotsData(host string) (*robotstxt.RobotsData, error) {
	r.robotsCacheMu.RLock()
	if r.robotsCache != nil {
		if cached, ok := r.robotsCache[host]; ok {
			r.robotsCacheMu.RUnlock()
			return cached, nil
		}
	}
	r.robotsCacheMu.RUnlock()

	// Fetch robots.txt - try HTTP first, then HTTPS if needed
	robotsURL := fmt.Sprintf("http://%s/robots.txt", host)
	req, err := http.NewRequest("GET", robotsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	var resp *http.Response
	if r.DisableRetries {
		resp, err = r.client().Do(req)
	} else {
		resp, err = r.client().DoWithRetry(req)
	}
	if err != nil {
		// Try HTTPS if HTTP fails
		robotsURL = fmt.Sprintf("https://%s/robots.txt", host)
		req, err = http.NewRequest("GET", robotsURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", r.UserAgent)
		if r.DisableRetries {
			resp, err = r.client().Do(req)
		} else {
			resp, err = r.client().DoWithRetry(req)
		}
		if err != nil {
			// No robots.txt or error fetching it
			robotsData := robotstxt.RobotsData{}
			r.cacheRobotsData(host, &robotsData)
			return &robotsData, nil
		}
	}
	defer closeQuietly(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// No robots.txt or error fetching it
		robotsData := robotstxt.RobotsData{}
		r.cacheRobotsData(host, &robotsData)
		return &robotsData, nil
	}

	robotsData, err := robotstxt.FromResponse(resp)
	if err != nil {
		return nil, err
	}

	r.cacheRobotsData(host, robotsData)
	return robotsData, nil
}

func (r *RAria2) cacheRobotsData(host string, data *robotstxt.RobotsData) {
	r.robotsCacheMu.Lock()
	if r.robotsCache == nil {
		r.robotsCache = make(map[string]*robotstxt.RobotsData)
	}
	r.robotsCache[host] = data
	r.robotsCacheMu.Unlock()
}
