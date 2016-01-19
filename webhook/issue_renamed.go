package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"hooksim/config"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/satori/go.uuid"
)

type IssueActorPair struct {
	Issue []byte
	Actor []byte
}

var (
	repoFields = [...]string{"id", "name", "full_name", "owner", "private", "html_url", "description", "fork", "url", "forks_url",
		"keys_url", "collaborators_url", "teams_url", "hooks_url", "issue_events_url", "events_url", "assignees_url",
		"branches_url", "tags_url", "blobs_url", "git_tags_url", "git_refs_url", "trees_url", "statuses_url", "languages_url",
		"stargazers_url", "contributors_url", "subscribers_url", "subscription_url", "commits_url", "git_commits_url", "comments_url",
		"issue_comment_url", "contents_url", "compare_url", "merges_url", "archive_url", "downloads_url", "issues_url", "pulls_url",
		"milestones_url", "notifications_url", "labels_url", "releases_url", "created_at", "updated_at", "pushed_at", "git_url", "ssh_url",
		"clone_url", "svn_url", "homepage", "size", "stargazers_count", "watchers_count", "language", "has_issues", "has_downloads",
		"has_wiki", "has_pages", "forks_count", "mirror_url", "open_issues_count", "forks", "open_issues", "watchers", "default_branch"}
)

func getRepoContent(owner, repo string, client *http.Client) string {
	resp, err := client.Get(fmt.Sprintf("%s/repos/%s/%s", config.GithubAPIURL, owner, repo))
	if err != nil {
		log.Printf("Error in getting repo content: %v\n", err)
		return "{}"
	}

	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error in reading repo content: %v\n", err)
		return "{}"
	}
	resp.Body.Close()

	var repoMap map[string]json.RawMessage
	if json.Unmarshal(content, &repoMap) != nil {
		log.Printf("Error in parsing repo content: %v\n", err)
		return "{}"
	}

	output := "{"
	for k, v := range repoFields {
		if k > 0 {
			output += ","
		}
		output += fmt.Sprintf("\"%s\":%s", v, string(repoMap[v]))
	}
	output += "}"

	return output
}

func TriggerIssueRenamedWebHook(owner, repo string, renamedIssues []IssueActorPair, queryClient *http.Client) {
	url, secret := getWebHookURL(owner, repo, "issues")
	if url == "" {
		return
	}

	for _, renamedIssue := range renamedIssues {
		payload := fmt.Sprintf("{\"action\":\"updated\",\"issue\":%s,\"repository\":%s,\"sender\":%s}",
			string(renamedIssue.Issue),
			getRepoContent(owner, repo, queryClient),
			string(renamedIssue.Actor))

		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(payload)))
		if err != nil {
			log.Printf("Error in creating POST request: %v\n", err)
		}

		req.Header.Add("User-Agent", "hooksim")
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Accept", "*/*")
		req.Header.Add("X-Github-Event", "issues")
		req.Header.Add("X-Github-Delivery", uuid.NewV4().String())
		if secret != "" {
			mac := hmac.New(sha1.New, []byte(secret))
			mac.Write([]byte(payload))
			req.Header.Add("X-Hub-Signature", fmt.Sprintf("sha1=%x", mac.Sum(nil)))
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Error in making webhook call: %v\n", err)
		}

		if resp.Body != nil {
			resp.Body.Close()
		}
	}
}
