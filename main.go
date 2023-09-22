// Musikant (German word for *musician*) adds the `hacktoberfest` topic to all of your public GitHub repositories,
// excluding forks and archived repositories.
//
// You have to provide an environment variable called `GITHUB_TOKEN` which contains
// a [personal access token](https://github.com/settings/personal-access-tokens/new).
// Ensure your token has sufficient access to all repositories that you want to change:
//
// | Repository permission | Access         |
// |-----------------------|----------------|
// | Administration        | Read and write |
// | Metadata              | Read-only      |
//
// Use `-mode remove` to remove the `hacktoberfest` topic again.
//
// If you don't want to make changes right away, use `-dry-run`.
//
// Note: GitHub's repo topics API is not transactional.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v55/github"
	"log"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
)

type AppContext struct {
	gitHub          *github.Client
	isAddMode       bool
	isDryRun        bool
	maxReposPerPage int
	maxPages        int
	maxWorkers      int
	affectedTopic   string
}

func (a *AppContext) getRepos() (allRepos []*github.Repository) {
	opt := &github.RepositoryListOptions{
		Visibility:  "public",
		Affiliation: "owner",
		ListOptions: github.ListOptions{PerPage: a.maxReposPerPage},
	}

	for i := 0; i < a.maxPages; i++ {
		repos, resp, err := a.gitHub.Repositories.List(context.Background(), "", opt)
		if err != nil {
			log.Fatal(err)
		}

		for _, repo := range repos {
			if repo.Fork == nil || *repo.Fork {
				continue
			}

			if repo.Archived == nil || *repo.Archived {
				continue
			}

			hasAffectedTopic := hasTopic(repo, a.affectedTopic)

			if !hasAffectedTopic && a.isAddMode {
				allRepos = append(allRepos, repo)
			}

			if hasAffectedTopic && !a.isAddMode {
				allRepos = append(allRepos, repo)
			}
		}

		if resp.NextPage == 0 {
			break
		}

		opt.Page = resp.NextPage
	}

	return
}

func (a *AppContext) updateTopicsOfRepos(repos []*github.Repository) {
	jobs := make(chan *github.Repository, len(repos))
	var wg sync.WaitGroup

	for i := 0; i < a.maxWorkers; i++ {
		wg.Add(1)

		go func(jobs <-chan *github.Repository) {
			defer wg.Done()

			for repo := range jobs {
				a.updateTopicsOfRepo(repo)
			}
		}(jobs)
	}

	for _, repo := range repos {
		jobs <- repo
	}

	close(jobs)
	wg.Wait()
}

func (a *AppContext) updateTopicsOfRepo(repo *github.Repository) {
	var newTopics []string

	if a.isAddMode {
		newTopics = append(repo.Topics, a.affectedTopic)
	}

	if !a.isAddMode {
		newTopics = removeTopic(repo.Topics, a.affectedTopic)
	}

	if repo.Owner == nil || repo.Owner.Login == nil || repo.Name == nil {
		log.Fatal("invalid owner, owner login or repo name")
	}

	if a.isDryRun {
		log.Printf("%q not updated (dry run): %s", *repo.Name, strings.Join(newTopics, " "))
		return
	}

	confirmedTopics, _, err := a.gitHub.Repositories.ReplaceAllTopics(
		context.Background(),
		*repo.Owner.Login,
		*repo.Name,
		newTopics,
	)
	if err != nil {
		log.Printf("%q failed: %s", *repo.Name, err)
	} else {
		log.Printf("%q updated: %s", *repo.Name, strings.Join(confirmedTopics, " "))
	}
}

func mapRepoNames(repos []*github.Repository) []string {
	repoNames := make([]string, len(repos))
	for i := range repos {
		repoNames[i] = *repos[i].Name
	}

	return repoNames
}

func hasTopic(repo *github.Repository, topic string) bool {
	return slices.Contains(repo.Topics, topic)
}

func removeTopic(topics []string, affectedTopic string) (newTopics []string) {
	for _, topic := range topics {
		if topic != affectedTopic {
			newTopics = append(newTopics, topic)
		}
	}

	return
}

const (
	addMode    = "add"
	removeMode = "remove"
)

func isAddMode(mode string) bool {
	if mode == addMode {
		return true
	}

	if mode == removeMode {
		return false
	}

	log.Fatal("unknown mode")

	return false
}

func newGitHubClient() *github.Client {
	rateLimit, err := github_ratelimit.NewRateLimitWaiterClient(http.DefaultTransport)
	if err != nil {
		log.Fatal(err)
	}

	return github.NewClient(rateLimit)
}

func main() {
	authToken := os.Getenv("GITHUB_TOKEN")

	mode := flag.String("mode", addMode, fmt.Sprintf("Desired operation: %q or %q", addMode, removeMode))
	dryRun := flag.Bool("dry-run", false, "Don't make any changes")
	maxWorkers := flag.Int("max-workers", runtime.NumCPU(), "Maximum number of concurrent requests to GitHub API")

	flag.Parse()

	appContext := AppContext{
		gitHub:          newGitHubClient().WithAuthToken(authToken),
		isAddMode:       isAddMode(*mode),
		isDryRun:        *dryRun,
		maxReposPerPage: 50,
		maxPages:        25,
		maxWorkers:      *maxWorkers,
		affectedTopic:   "hacktoberfest",
	}

	repos := appContext.getRepos()
	log.Printf("Changing the topic %q for the following repos: %s", appContext.affectedTopic, strings.Join(mapRepoNames(repos), " "))

	appContext.updateTopicsOfRepos(repos)
}
