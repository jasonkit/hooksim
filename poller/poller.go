package poller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hooksim/config"
	"hooksim/webhook"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type LastAccess struct {
	ETag    string
	EventID uint64
}

type Poller struct {
	Clients  map[string]*http.Client
	Repos    map[string]map[string]LastAccess
	NumRepo  int
	Interval time.Duration

	StopReqCh  chan bool
	StopDoneCh chan bool
}

// New takes the polling interval in second and return a pointer to Poller
// It will also load the data store in the path indicated in config.DataDir so that
// the poller restore the ETag and last retrieved issue event id from its last query on each repo
func New(interval int) *Poller {
	poller := &Poller{
		Clients:    make(map[string]*http.Client),
		Repos:      make(map[string]map[string]LastAccess),
		Interval:   time.Duration(interval) * time.Second,
		StopReqCh:  make(chan bool),
		StopDoneCh: make(chan bool),
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

// Stop will stop the poller task
func (p *Poller) Stop() {
	p.StopReqCh <- true
}

// Run will start the poller task, this call will block until Stop() is called
func (p *Poller) Run() {
	delay := p.Interval / time.Duration(p.NumRepo)

	defer func() {
		p.StopDoneCh <- true
	}()

	for {
		for owner, v := range p.Repos {
			for repo := range v {
				select {
				case <-p.StopReqCh:
					return
				case <-time.After(delay):
					if pairs := p.pollRepo(owner, repo); len(pairs) > 0 {
						webhook.TriggerIssueRenamedWebHook(owner, repo, pairs, p.Clients[owner])
					}
				}
			}
		}
	}

}

// pollRepo makes query to /repos/:user/:repo/issues/events, scan for unread issue event
// if there is unread "renamed" issue event, it will enqueue the correspond issue and actor
// content pair to a queue, this queue will be output of this method. By exemine the length
// of the queue, we can decide we should trigger webhook call or not.
//
// GET query which without "page" param will have If-None-Match in the request header so as to
// speed up the query and reduce the comsumption of github API quota
func (p *Poller) pollRepo(owner, repo string) []webhook.IssueActorPair {
	if config.Verbose {
		fmt.Printf("polling %s/%s...\n", owner, repo)
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/%s/issues/events", config.GithubAPIURL, owner, repo), nil)
	if err != nil {
		log.Printf("Error in creating GET request:%v\n, err")
		return nil
	}

	if p.Repos[owner][repo].ETag != "" {
		req.Header.Add("If-None-Match", p.Repos[owner][repo].ETag)
	}

	client := p.Clients[owner]
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error in get issue events:%v\n, err")
		return nil
	}

	if resp.StatusCode == 304 {
		if resp.Body != nil {
			resp.Body.Close()
		}
		return nil
	}

	page := 1
	etag := resp.Header.Get("ETag")
	var latestID uint64
	var maxPage int
	var pairs []webhook.IssueActorPair

	for {
		foundLastAccess, latestIDInPage, pairsInPage, err := p.parseResponse(owner, repo, resp)

		if maxPage == 0 && resp.Header.Get("Link") != "" {
			maxPage = getMaxPage(resp.Header.Get("Link"))
		}

		if latestIDInPage > latestID {
			latestID = latestIDInPage
		}

		if err != nil {
			log.Printf("Error in parsing response: %v", err)
			return nil
		}

		if foundLastAccess || p.Repos[owner][repo].EventID == 0 {
			pairs = append(pairs, pairsInPage...)
			break
		} else {
			if resp.Body != nil {
				resp.Body.Close()
			}

			page++
			if page > maxPage {
				break
			}

			req, err = http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/%s/issues/events?page=%d", config.GithubAPIURL, owner, repo, page), nil)
			if err != nil {
				log.Printf("Error in creating GET request:%v\n, err")
				return nil
			}
			resp, err = client.Do(req)
			if err != nil {
				log.Printf("Error in get issue events:%v\n, err")
				return nil
			}
		}
	}

	a := LastAccess{ETag: etag, EventID: latestID}
	p.Repos[owner][repo] = a
	storeLastAccess(owner, repo, a)

	if config.Verbose {
		if len(pairs) > 0 {
			fmt.Printf("New rename event detected.\n")
		}
	}

	return pairs
}

// parseResponse is the helper function for pollRepo, it is used to scan for unread renamed issue
// event and return the correspond issue and actor content pair
// It will also indicated whether it is time to stop query the next page by comparing the event ID
// with this stored one
func (p *Poller) parseResponse(owner, repo string, resp *http.Response) (bool, uint64, []webhook.IssueActorPair, error) {
	var pairs []webhook.IssueActorPair

	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, 0, nil, err
	}

	var result []json.RawMessage
	if err := json.Unmarshal(content, &result); err != nil {
		return false, 0, nil, err
	}

	var latestID uint64
	lastID := p.Repos[owner][repo].EventID
	foundLastAccess := false

	for _, v := range result {
		var event map[string]interface{}
		if err := json.Unmarshal(v, &event); err != nil {
			return false, 0, nil, err
		}

		curID := uint64(event["id"].(float64))
		eventType := event["event"].(string)

		if curID > latestID {
			latestID = curID
		}

		if lastID >= curID || lastID == 0 {
			foundLastAccess = true
			break
		}

		if config.Verbose {
			fmt.Printf("> curID:%v lastID:%v event:%v\n", curID, lastID, eventType)
		}

		if eventType == "renamed" {
			var event map[string]json.RawMessage
			json.Unmarshal(v, &event)
			pairs = append(pairs, webhook.IssueActorPair{Issue: []byte(event["issue"]), Actor: []byte(event["actor"])})
		}

	}

	return foundLastAccess, latestID, pairs, nil
}

// getMaxPage parse the content of Link Header and extact the max page number
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

// restoreLastAccess load the ETag and latest seen issue event ID from local storage
func restoreLastAccess(owner, repo string) LastAccess {
	content, err := ioutil.ReadFile(path.Join(config.DataDir, owner, repo))
	if err != nil {
		return LastAccess{"", 0}
	}

	buf := bytes.NewBuffer(content)
	etag, err := buf.ReadString('\n')
	if err != nil {
		return LastAccess{"", 0}
	}
	etag = strings.Trim(etag, "\n ")

	idStr, err := buf.ReadString('\n')
	if err != nil {
		return LastAccess{"", 0}
	}
	idStr = strings.Trim(idStr, "\n ")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return LastAccess{"", 0}
	}

	return LastAccess{etag, id}
}

// storeLastAccess save the ETag and latest seen issue event ID to local storage
func storeLastAccess(owner, repo string, a LastAccess) {
	errFmt := "Error in storing last access infomation: %v\n"

	err := os.MkdirAll(path.Join(config.DataDir, owner), 0755)
	if err != nil {
		log.Printf(errFmt, err)
		return
	}

	content := fmt.Sprintf("%v\n%v\n", a.ETag, a.EventID)
	err = ioutil.WriteFile(path.Join(config.DataDir, owner, repo), []byte(content), 0644)
	if err != nil {
		log.Printf(errFmt, err)
	}
}
