package config

import (
	"encoding/json"
	"io/ioutil"
)

type Account struct {
	User  string
	Token string
	Hooks []Hook
}

type Hook struct {
	Repo   string
	Events []string
	URL    string
	Secret string
}

var Accounts []Account

func Load(path string) error {
	confInJSON, err := ioutil.ReadFile(path)

	if err != nil {
		return err
	}

	err = json.Unmarshal(confInJSON, &Accounts)
	if err != nil {
		return err
	}

	for i, acct := range Accounts {
		for j, hook := range acct.Hooks {
			containWildcard := false
			for _, event := range hook.Events {
				if event == "*" {
					containWildcard = true
					break
				}
			}

			if containWildcard {
				Accounts[i].Hooks[j].Events = []string{"*"}
			} else if len(hook.Events) == 0 {
				Accounts[i].Hooks[j].Events = []string{"push"}
			}
		}
	}

	return nil
}
