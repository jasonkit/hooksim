package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hooksim/config"
	"hooksim/types"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

type LastAccess struct {
	ETag    string
	EventID uint64
}

type Worker struct {
	Client     *http.Client
	LastAccess LastAccess
	Owner      string
	Repo       string
}

// New takes the repo owner,repo name, and the http client with oauth2 header set up
// to creates a new Worker and return the pointer to it.
// It will also resotre the last access info (ETag and last issue event ID) from local storage
func New(owner, repo string, client *http.Client) *Worker {
	w := &Worker{
		Owner:  owner,
		Repo:   repo,
		Client: client,
	}
	w.loadLastAccess()
	return w
}

// getIssueEvent makes HTTP GET request to fetch list of Issue Event in the specific page.
func (worker *Worker) getIssueEvent(page int, useETag bool) (*http.Response, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/%s/issues/events?page=%d", config.GithubAPIURL, worker.Owner, worker.Repo, page), nil)
	if err != nil {
		return nil, err
	}

	if useETag && worker.LastAccess.ETag != "" {
		req.Header.Add("If-None-Match", worker.LastAccess.ETag)
	}

	resp, err := worker.Client.Do(req)
	if err != nil {
		log.Printf("Error in get issue events:%v\n, err")
		return nil, err
	}

	return resp, nil
}

// PollRepo makes query to /repos/:user/:repo/issues/events, scan for unread issue event
// if there is unread "renamed" issue event, it will enqueue the correspond issue and actor
// content pair to a queue, this queue will be output of this method. By exemine the length
// of the queue, we can decide we should trigger webhook call or not.
//
// GET query which with param "page=1" will have If-None-Match in the request header so as to
// speed up the query and reduce the comsumption of github API quota
func (worker *Worker) PollRepo() []types.IssueActorPair {
	if config.Verbose {
		fmt.Printf("polling %s/%s...\n", worker.Owner, worker.Repo)
	}

	resp, err := worker.getIssueEvent(1, true)
	if err != nil {
		log.Printf("Error in getting issue event: %v\n", err)
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
	var pairs []types.IssueActorPair

	for {
		foundLastAccess, latestIDInPage, pairsInPage, err := worker.parseResponse(resp)

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

		if foundLastAccess || worker.LastAccess.EventID == 0 {
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

			resp, err = worker.getIssueEvent(page, false)
		}
	}

	worker.updateLastAccess(etag, latestID)

	if config.Verbose {
		if len(pairs) > 0 {
			fmt.Printf("New rename event detected.\n")
		}
	}

	return pairs
}

// parseResponse is the helper function for PollRepo, it is used to scan for unread renamed issue
// event and return the correspond issue and actor content pair
// It will also indicated whether it is time to stop query the next page by comparing the event ID
// with this stored one
func (worker *Worker) parseResponse(resp *http.Response) (foundLastAccess bool, latestID uint64, pairs []types.IssueActorPair, err error) {
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, 0, nil, err
	}

	var result []json.RawMessage
	if err := json.Unmarshal(content, &result); err != nil {
		return false, 0, nil, err
	}

	lastID := worker.LastAccess.EventID
	foundLastAccess = false

	for _, v := range result {
		var event map[string]json.RawMessage
		if err := json.Unmarshal(v, &event); err != nil {
			return false, 0, nil, err
		}

		var curID uint64
		var eventType string
		json.Unmarshal(event["id"], &curID)
		json.Unmarshal(event["event"], &eventType)

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
			pairs = append(pairs, types.IssueActorPair{Issue: []byte(event["issue"]), Actor: []byte(event["actor"])})
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

// updateLastAccess updates worker's last access info and save it to the local storage
func (worker *Worker) updateLastAccess(etag string, eventID uint64) {
	worker.LastAccess.ETag = etag
	worker.LastAccess.EventID = eventID
	worker.saveLastAccess()
}

// loadLastAccess load the ETag and latest seen issue event ID from local storage
func (worker *Worker) loadLastAccess() {
	content, err := ioutil.ReadFile(path.Join(config.DataDir, worker.Owner, worker.Repo))
	if err != nil {
		return
	}

	buf := bytes.NewBuffer(content)
	etag, err := buf.ReadString('\n')
	if err != nil {
		return
	}
	etag = strings.Trim(etag, "\n ")

	idStr, err := buf.ReadString('\n')
	if err != nil {
		return
	}
	idStr = strings.Trim(idStr, "\n ")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return
	}

	worker.LastAccess.ETag = etag
	worker.LastAccess.EventID = id
}

// saveLastAccess save the ETag and latest seen issue event ID to local storage
func (worker *Worker) saveLastAccess() {
	errFmt := "Error in storing last access infomation: %v\n"

	err := os.MkdirAll(path.Join(config.DataDir, worker.Owner), 0755)
	if err != nil {
		log.Printf(errFmt, err)
		return
	}

	content := fmt.Sprintf("%v\n%v\n", worker.LastAccess.ETag, worker.LastAccess.EventID)
	err = ioutil.WriteFile(path.Join(config.DataDir, worker.Owner, worker.Repo), []byte(content), 0644)
	if err != nil {
		log.Printf(errFmt, err)
	}
}
