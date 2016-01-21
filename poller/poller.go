package poller

import (
	"hooksim/config"
	pollerWorker "hooksim/poller/worker"
	"hooksim/webhook"
	"time"

	"golang.org/x/oauth2"
)

type Poller struct {
	Workers  []*pollerWorker.Worker
	Interval time.Duration

	StopReqCh  chan bool
	StopDoneCh chan bool
}

// New takes the polling interval in second and return a pointer to Poller
// Poller contains several workers to do the actual repo polling jobs, number of worker
// is same as number of repo being specified in the config file.
func New(interval int) *Poller {
	poller := &Poller{
		Interval:   time.Duration(interval) * time.Second,
		StopReqCh:  make(chan bool),
		StopDoneCh: make(chan bool),
	}

	for _, acct := range config.Accounts {
		client := oauth2.NewClient(oauth2.NoContext, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: acct.Token}))
		for _, hook := range acct.Hooks {
			poller.Workers = append(poller.Workers, pollerWorker.New(acct.User, hook.Repo, client))
		}
	}

	return poller
}

// Stop will stop the poller task
func (poller *Poller) Stop() {
	poller.StopReqCh <- true
}

// Run will start the poller task, this call will block until Stop() is called
func (poller *Poller) Run() {
	delay := poller.Interval / time.Duration(len(poller.Workers))

	defer func() {
		poller.StopDoneCh <- true
	}()

	for {
		for _, worker := range poller.Workers {
			select {
			case <-poller.StopReqCh:
				return
			case <-time.After(delay):
				if issueActorPairs := worker.PollRepo(); len(issueActorPairs) > 0 {
					webhook.TriggerIssueRenamedWebHook(worker.Owner, worker.Repo, issueActorPairs, worker.Client)
				}
			}
		}
	}

}
