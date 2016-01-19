package poller

import (
	"encoding/json"
	"fmt"
	"hooksim/config"
	"hooksim/webhook"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	apiUrl = "https://api.github.com"
)

type LastAccess struct {
	ETag      string
	Timestamp uint64
}

type Poller struct {
	Clients  map[string]*http.Client
	Repos    map[string]map[string]LastAccess
	NumRepo  int
	Interval time.Duration
}

func New(interval int) *Poller {
	poller := &Poller{
		Clients:  make(map[string]*http.Client),
		Repos:    make(map[string]map[string]LastAccess),
		Interval: time.Duration(interval) * time.Second,
	}

	for _, acct := range config.Accounts {
		client := oauth2.NewClient(oauth2.NoContext, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: acct.Token}))
		poller.Clients[acct.User] = client

		poller.Repos[acct.User] = make(map[string]LastAccess)
		for _, hook := range acct.Hooks {
			poller.Repos[acct.User][hook.Repo] = restoreLastAccess(acct.User, hook.Repo)
		}
		poller.NumRepo += len(poller.Repos[acct.User])
	}

	return poller
}

func (p *Poller) Run() {
	delay := p.Interval / time.Duration(p.NumRepo)
	for {
		for owner, v := range p.Repos {
			for repo := range v {
				if renamedIssue, actor := p.pollRepo(owner, repo); renamedIssue != nil {
					webhook.TriggerIssueRenamedWebHook(owner, repo, renamedIssue, actor, p.Clients[owner])
				}
				time.Sleep(delay)
			}
		}
	}
}

func (p *Poller) pollRepo(owner, repo string) ([]byte, []byte) {
	fmt.Printf("polling %s/%s...\n", owner, repo)

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/%s/issues/events", apiUrl, owner, repo), nil)
	if err != nil {
		log.Printf("Error in creating GET request:%v\n, err")
		return nil, nil
	}

	if p.Repos[owner][repo].ETag != "" {
		req.Header.Add("If-None-Match", p.Repos[owner][repo].ETag)
	}

	client := p.Clients[owner]
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error in get issue events:%v\n, err")
		return nil, nil
	}

	if resp.StatusCode == 304 {
		if resp.Body != nil {
			resp.Body.Close()
		}
		return nil, nil
	}

	page := 1
	etag := resp.Header.Get("ETag")
	hasNewRenamedEvent := false
	var newRenamedIssue, eventActor []byte
	var latestTs uint64
	var maxPage int

	for {
		foundLastAccess, hasRenamedEvent, latestPageTs, renamedIssue, actor, err := p.parseResponse(owner, repo, resp)

		if maxPage == 0 && resp.Header.Get("Link") != "" {
			maxPage = getMaxPage(resp.Header.Get("Link"))
		}

		if latestPageTs > latestTs {
			latestTs = latestPageTs
		}

		if err != nil {
			log.Printf("Error in parsing response: %v", err)
			return nil, nil
		}

		// As we are searching from newest to oldest, whenever hasRenamedEvent is true,
		// it must be a new renamed event
		if foundLastAccess || hasRenamedEvent || p.Repos[owner][repo].Timestamp == 0 {
			if hasRenamedEvent {
				hasNewRenamedEvent = true
				newRenamedIssue = renamedIssue
				eventActor = actor
			}
			break
		} else {
			if resp.Body != nil {
				resp.Body.Close()
			}

			page++
			if page > maxPage {
				break
			}

			req, err = http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/%s/issues/events?page=%d", apiUrl, owner, repo, page), nil)
			if err != nil {
				log.Printf("Error in creating GET request:%v\n, err")
				return nil, nil
			}
			resp, err = client.Do(req)
			if err != nil {
				log.Printf("Error in get issue events:%v\n, err")
				return nil, nil
			}
		}
	}

	a := LastAccess{ETag: etag, Timestamp: latestTs}
	p.Repos[owner][repo] = a
	storeLastAccess(owner, repo, a)

	if hasNewRenamedEvent {
		fmt.Printf("New Renamed Event!!\n")
	}

	return newRenamedIssue, eventActor
}

func (p *Poller) parseResponse(owner, repo string, resp *http.Response) (bool, bool, uint64, []byte, []byte, error) {
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, false, 0, nil, nil, err
	}

	var result []json.RawMessage
	if err := json.Unmarshal(content, &result); err != nil {
		return false, false, 0, nil, nil, err
	}

	var latestTs uint64
	lastTs := p.Repos[owner][repo].Timestamp
	foundLastAccess := false
	hasRenamedEvent := false
	var renamedIssue, actor []byte

	for _, v := range result {
		var event map[string]json.RawMessage
		if err := json.Unmarshal(v, &event); err != nil {
			return false, false, 0, nil, nil, err
		}

		var tsStr, eventType string
		json.Unmarshal(event["created_at"], &tsStr)
		json.Unmarshal(event["event"], &eventType)

		ts, _ := time.Parse(time.RFC3339, tsStr)
		tsUint64 := uint64(ts.Unix())

		if tsUint64 > latestTs {
			latestTs = tsUint64
		}

		if eventType == "renamed" {
			hasRenamedEvent = true
			renamedIssue = []byte(event["issue"])
			actor = []byte(event["actor"])
		}

		if lastTs > tsUint64 {
			foundLastAccess = true
			break
		}
	}

	return foundLastAccess, hasRenamedEvent, latestTs, renamedIssue, actor, nil
}

func getMaxPage(link string) int {
	lastPageURL, err := url.Parse(strings.Trim(strings.Split(strings.Split(link, ",")[1], ";")[0], " <>"))
	if err != nil {
		log.Printf("Error when parsing last page url: %v\n", err)
		return 0
	}
	maxPage, err := strconv.Atoi(lastPageURL.Query().Get("page"))
	if err != nil {
		log.Printf("Error when parsing last page url: %v\n", err)
		return 0
	}

	return maxPage
}

func restoreLastAccess(owner, repo string) LastAccess {
	return LastAccess{"", 0}
}

func storeLastAccess(owner, repo string, a LastAccess) {
}
