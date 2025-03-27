// workflows-hasher is a simple tool that replaces version tags from external actions with their respective SHA hash.
//
// It handles actions from `jobs.<job_id>.steps[*].uses` in the format of `owner/repo@v1234` hosted on GitHub,
// which is sufficient for many workflows.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"

	"github.com/die-net/lrucache"
	"github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v70/github"
	"github.com/gregjones/httpcache"
	"gopkg.in/yaml.v3"
)

var usesExpression = regexp.MustCompile("^([a-zA-Z0-9_.-]+)/([a-zA-Z0-9_.-]+)@(v[a-zA-Z0-9_.-]+)$")

func replaceUsesVersionTagWithSHA(ctx context.Context, gitHub *github.Client, uses string) (string, error) {
	matches := usesExpression.FindAllStringSubmatch(uses, 1)
	if len(matches) != 1 {
		return "", fmt.Errorf("invalid number of matches: %v", len(matches))
	}

	match := matches[0]
	if len(match) != 4 {
		return "", fmt.Errorf("invalid number of submatches: %v", len(match))
	}

	owner := match[1]
	repo := match[2]
	ref := match[3]

	commit, _, err := gitHub.Repositories.GetCommit(ctx, owner, repo, ref, nil)
	if err != nil {
		return "", fmt.Errorf("error getting commit: %v", err)
	}

	newRef := commit.GetSHA()
	if newRef == "" {
		return "", errors.New("missing SHA for ref")
	}

	return fmt.Sprintf("%s/%s@%s", owner, repo, newRef), nil
}

func processYAMLUses(sourceFileName, targetFileName string, processFunc func(string) string) error {
	data, err := os.ReadFile(sourceFileName)
	if err != nil {
		return fmt.Errorf("error reading file: %v", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("error parsing YAML: %v", err)
	}

	modified := processUsesInAST(&doc, processFunc)

	if modified {
		updatedYAML, err := yaml.Marshal(&doc)
		if err != nil {
			return fmt.Errorf("error marshaling modified YAML: %v", err)
		}

		fileStat, err := os.Stat(sourceFileName)
		if err != nil {
			return err
		}

		if err := os.WriteFile(targetFileName, updatedYAML, fileStat.Mode().Perm()); err != nil {
			return fmt.Errorf("error writing modified file: %v", err)
		}
	}

	return nil
}

func processUsesInAST(node *yaml.Node, processFunc func(string) string) bool {
	modified := false

	for i := 0; i < len(node.Content); i++ {
		currentNode := node.Content[i]

		if currentNode.Kind == yaml.ScalarNode && currentNode.Value == "uses" {
			if i+1 < len(node.Content) {
				valueNode := node.Content[i+1]
				originalValue := valueNode.Value
				newValue := processFunc(originalValue)

				if newValue != originalValue {
					valueNode.Value = newValue
					modified = true
				}
			}
		}

		if currentNode.Content != nil {
			if childModified := processUsesInAST(currentNode, processFunc); childModified {
				modified = true
			}
		}
	}

	return modified
}

type AppContext struct {
	gitHub             *github.Client
	workflowsDirectory string
	overwriteFiles     bool
}

func (a *AppContext) IterateWorkflowFiles(ctx context.Context) error {
	entries, err := os.ReadDir(a.workflowsDirectory)
	if err != nil {
		return fmt.Errorf("error reading directory: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		sourceFileName := path.Join(a.workflowsDirectory, entry.Name())
		targetFileName := os.Stdout.Name()

		if a.overwriteFiles {
			targetFileName = sourceFileName
		}

		log.Printf("processing %q\n", sourceFileName)

		if err := processYAMLUses(sourceFileName, targetFileName, func(uses string) string {
			newUses, err := replaceUsesVersionTagWithSHA(ctx, a.gitHub, uses)
			if err != nil {
				log.Print(err)
				return uses
			}

			return newUses
		}); err != nil {
			return err
		}
	}

	return nil
}

func newGitHubClient(authToken string) *github.Client {
	rateLimit, err := github_ratelimit.NewRateLimitWaiterClient(httpcache.NewTransport(lrucache.New(1000, int64(3600))))
	if err != nil {
		log.Fatal(err)
	}

	client := github.NewClient(rateLimit)

	if authToken != "" {
		return client.WithAuthToken(authToken)
	}

	return client
}

func main() {
	authToken := os.Getenv("GITHUB_TOKEN")

	workflowsDirectory := flag.String("workflows-directory", "", "The directory that contains your GitHub workflow YAML files.")
	overwriteFiles := flag.Bool("overwrite-files", false, "Overwrite the workflow files.")

	flag.Parse()

	if *workflowsDirectory == "" {
		log.Fatal("-workflows-directory is required")
	}

	appContext := AppContext{
		gitHub:             newGitHubClient(authToken),
		workflowsDirectory: *workflowsDirectory,
		overwriteFiles:     *overwriteFiles,
	}

	ctx := context.Background()

	if err := appContext.IterateWorkflowFiles(ctx); err != nil {
		log.Fatal(err)
	}
}
