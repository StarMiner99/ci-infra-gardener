package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/prow/pkg/git/v2"
	"sigs.k8s.io/prow/pkg/github"
)

const commitTitle = "Update OWNERS_ALIASES from Peribolos config"
const commitBody = `
Automated update by owners-aliases-bumper. Alias membership was synced with the GitHub team definitions in the Peribolos config.
`
const prBranch = "owners-aliases-bumper"
const prTitle = commitTitle
const prBody = `
<!-- Please ensure that you do not include company internal information. -->

**How to categorize this PR?**
<!--
Please select the kind of this pull request, e.g.:
/kind enhancement

Tide will not merge your PR, if it is missing a kind/* label.
"/kind" identifiers:    api-change|bug|cleanup|discussion|enhancement|epic|impediment|poc|post-mortem|question|regression|task|technical-debt|test
-->
/kind cleanup

**What this PR does / why we need it**:
Automated update by owners-aliases-bumper. The alias membership in this
repo's OWNERS_ALIASES file has been synced to match the GitHub team
definitions in the Peribolos config.

Aliases that share a name with a Peribolos team are populated from that
team's members and maintainers. Only aliases already present in
OWNERS_ALIASES are updated — no aliases are added or removed. This keeps
OWNERS_ALIASES in sync with the source-of-truth team definitions so
approvers/reviewers don't drift from actual team membership.

**Special notes for your reviewer**:
This PR was generated automatically.
`

func forkAndCheckoutRepo(ghClient github.Client, gitClient git.ClientFactory, orgName, repoName string) (git.RepoClient, error) {
	r, err := gitClient.ClientFor(orgName, repoName)

	if err != nil {
		return nil, err
	}

	repoInfo, err := ghClient.GetRepo(orgName, repoName)

	if err != nil {
		return nil, err
	}

	r.Checkout(repoInfo.DefaultBranch)
	r.CheckoutNewBranch(prBranch)

	return r, nil
}

func commitAndPush(ghClient github.Client, repoClient git.RepoClient, orgName, repoName string) error {
	if err := repoClient.Commit(commitTitle, commitBody); err != nil {
		return errors.Join(err, fmt.Errorf("Failed to commit to repo %s/%s", orgName, repoName))
	}

	if err := repoClient.PushToCentral(prBranch, true); err != nil {
		return errors.Join(err, fmt.Errorf("Failed to push to repo %s/%s", orgName, repoName))
	}

	return nil
}

func findOrCreatePR(ghClient github.Client, orgName, repoName string) (int, error) {
	repoInfo, err := ghClient.GetRepo(orgName, repoName)

	if err != nil {
		return 0, errors.Join(err, fmt.Errorf("Failed to get Repo %s/%s", orgName, repoName))
	}

	prs, err := ghClient.GetPullRequests(orgName, repoName)

	if err != nil {
		return 0, errors.Join(err, fmt.Errorf("Failed to get PRs for repo %s/%s", orgName, repoName))
	}

	// filter all prs that are not open and all prs that do not match our branch
	prs = slices.DeleteFunc(prs, func(pr github.PullRequest) bool {
		return pr.Head.Ref != prBranch || pr.State != "open"
	})

	var prNum int
	// no open PR
	if len(prs) < 1 {
		prNum, err = ghClient.CreatePullRequest(orgName, repoName, prTitle, prBody, prBranch, repoInfo.DefaultBranch, false)
		if err != nil {
			return 0, errors.Join(err, fmt.Errorf("Failed to create PR for repo %s/%s", orgName, repoName))
		}
	}
	// one open PR
	if len(prs) == 1 {
		prNum = prs[0].Number
	}
	// more than one open PR
	if len(prs) > 1 {
		prNum = prs[0].Number
		// close all other PRs, someone must have opened a PR manually
		for _, pr := range prs[1:] {
			if err := ghClient.ClosePullRequest(orgName, repoName, pr.Number); err != nil {
				logrus.WithError(err).Warnf("Failed closing PR %d from repo %s/%s", pr.Number, orgName, repoName)
			}
		}
	}

	return prNum, nil
}
