# hooksim
go program to redirect github webhook call and generate additional webhook call when github issue's description get updated.

## Build
Make sure hooksim is in the GOPATH, then simply run ```go build```

## Usgae
```
Usage of ./hooksim:
  -c string
    	Path to config file (default "config.json")
  -d string
    	Path to data directory (default "./data")
  -i int
    	Polling interval (in seconds) for all repositories (default 5s)
  -p int
    	Listening port (default 9000)
  -v	Verbose
```

## Config file
```
[
  {
    "user": <YOUR_GITHUB_USERNANE>,
    "token": <YOUR_GITHB_PERSONAL_ACCESS_TOKEN>,
    "hooks": [
      {
        "repo": <REPO_NAME_OWNED_BY_USER>,
        "event": ["*"],
        "url": <WEBHOOK_ENDPOINT>,
        "secret": <WEBHOOK_SECRET>
      }...
    ]
  }...
]
```
* Make sure the pesonal access token has permisson to read the interested repositories.
* Value of event can be found [here](https://developer.github.com/webhooks/#events)
* secret key use to compute the sha1 HMAC for the webhook payload, which can be found in the X-Hub-Signature
