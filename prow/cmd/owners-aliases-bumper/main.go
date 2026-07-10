package main

import (
	"path/filepath"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/prow/pkg/logrusutil"
)

func main() {
	logrusutil.ComponentInit()

	o := parseOptions()

	cfg := newFullOrgAliases()

	orgConfig := parseOrgConfig(o.peribolosConfig)

	ghClient, err := o.ghOpts.GitHubClient(!o.applyChanges)

	if err != nil {
		logrus.WithError(err).Fatal("Failed to initialize GitHub client!")
	}

	gitClient, err := o.ghOpts.GitClientFactory("", nil, !o.applyChanges, false)

	if err != nil {
		logrus.WithError(err).Fatal("Failed to initialize git client!")
	}

	// build our available aliases from the teams information we have
	for orgName, orgConfig := range orgConfig.Orgs {
		addMembersFromTeams(cfg.getConfig(orgName), orgConfig.Teams, "")
	}

	// remove repos that should be skipped
	removeExcludedRepos(&orgConfig, o.skipRepos.Strings())

	// manage every repo defined in peribolos conf
	for orgName, orgConfig := range orgConfig.Orgs {
		for repoName := range orgConfig.Repos {
			changes, changed := calculateAliasChanges(ghClient, cfg, orgName, repoName)
			if changes == nil || !changed {
				continue // skip change if not applicable
			}

			// download repo
			repoClient, err := forkAndCheckoutRepo(ghClient, gitClient, orgName, repoName)
			if err != nil {
				logrus.WithError(err).Errorf("Failed to initialize Git Client for %s/%s", orgName, repoName)
				continue
			}

			// write changes to file
			repoDir := repoClient.Directory()
			aliasesPath := filepath.Join(repoDir, "OWNERS_ALIASES")

			if err := writeChanges(aliasesPath, changes); err != nil {
				logrus.WithError(err).Errorf("Failed to write changes to OWNERS_ALIASES of repo %s/%s", orgName, repoName)
				continue
			}

			// commit and push changes
			if err := commitAndPush(repoClient, orgName, repoName); err != nil {
				logrus.WithError(err).Errorf("Commit and push failed repo: %s/%s", orgName, repoName)
				continue
			}

			// open PR
			id, err := findOrCreatePR(ghClient, orgName, repoName)
			if err != nil {
				logrus.WithError(err).Errorf("Opening PR failed on repo %s/%s", orgName, repoName)
				continue
			}

			logrus.Infof("Successfully applied changes to %s/%s and opened PR #%d", orgName, repoName, id)

			// cleanup
			if err := repoClient.Clean(); err != nil {
				logrus.WithError(err).Errorf("Failed to delete/cleanup locally stored repo: %s/%s", orgName, repoName)
			}
		}
	}
}
