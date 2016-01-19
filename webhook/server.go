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
	"strings"

	"github.com/tylerb/graceful"
)

func getRepoNameAndOwner(payload []byte) (repoName, owner string, err error) {
	var event map[string]json.RawMessage
	var repo map[string]json.RawMessage

	err = nil
	err = json.Unmarshal(payload, &event)
	if err != nil {
		return "", "", err
	}

	err = json.Unmarshal(event["repository"], &repo)
	if err != nil {
		return "", "", err
	}

	var fullname string
	err = json.Unmarshal(repo["full_name"], &fullname)
	if err != nil {
		return "", "", err
	}

	fields := strings.Split(fullname, "/")
	return fields[1], fields[0], err
}

func getWebHookURL(owner, repo, event string) (string, string) {
	for _, acct := range config.Accounts {
		if acct.User != owner {
			continue
		}

		for _, hook := range acct.Hooks {
			if hook.Repo != repo {
				continue
			}

			if len(hook.Events) == 1 && hook.Events[0] == "*" {
				return hook.URL, hook.Secret
			}

			for _, e := range hook.Events {
				if e == event {
					return hook.URL, hook.Secret
				}
			}
		}
	}
	return "", ""
}

func handleHook(w http.ResponseWriter, r *http.Request) {
	payload, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error in reading webhook payload: %v\n", err)
		return
	}
	r.Body.Close()

	repo, owner, err := getRepoNameAndOwner(payload)
	if err != nil {
		log.Printf("Error in unmarshalling webhook payload: %v\n", err)
		return
	}

	event := r.Header.Get("X-Github-Event")

	url, _ := getWebHookURL(owner, repo, event)
	if url == "" {
		return
	}

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("Error in creating POST request: %v\n", err)
	}

	req.Header.Add("User-Agent", r.Header.Get("User-Agent"))
	req.Header.Add("Content-Type", r.Header.Get("Content-Type"))
	req.Header.Add("Accept", r.Header.Get("Accept"))
	req.Header.Add("X-Github-Event", event)
	req.Header.Add("X-Github-Delivery", r.Header.Get("X-Github-Delivery"))

	if signature := r.Header.Get("X-Hub-Signature"); signature != "" {
		req.Header.Add("X-Hub-Signature", signature)
	}

	fmt.Printf("Redirecting Webhook call.\n")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error in making webhook call: %v\n", err)
	}

	if resp.Body != nil {
		resp.Body.Close()
	}
}

func handleHookTester(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Receive WebHook Call:\n")
	fmt.Printf("[Header]\n")
	for k, v := range r.Header {
		fmt.Printf("\t%v: %s\n", k, v)
	}

	content, _ := ioutil.ReadAll(r.Body)
	fmt.Printf("[Body]\n%s\n", string(content))
	mac := hmac.New(sha1.New, []byte("test1234"))
	mac.Write(content)
	fmt.Printf("chksum:%x\n", mac.Sum(nil))
	r.Body.Close()
}

func Server(port int) *graceful.Server {
	mux := http.NewServeMux()

	server := &graceful.Server{
		Server: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: mux,
		},
	}

	mux.HandleFunc("/hook", handleHook)
	mux.HandleFunc("/hookTester", handleHookTester)

	return server
}
