package main

import (
	"path/filepath"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/prow/pkg/logrusutil"
)

func main() {
	logrusutil.ComponentInit()

	o := parseOptions()

	if o.applyChanges {
		logrus.Info("Running in APPLY mode (--confirm set): changes will be pushed and PRs opened")
	} else {
		logrus.Info("Running in DRY-RUN mode (no --confirm): no forks, commits or PRs will be created")
	}

	cfg := newFullOrgAliases()

	logrus.Infof("Reading Peribolos config from %s", o.peribolosConfig)
	orgConfig := parseOrgConfig(o.peribolosConfig)
	logrus.Infof("Parsed Peribolos config: %d org(s) defined", len(orgConfig.Orgs))

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
		aliases := cfg.getConfig(orgName)
		addMembersFromTeams(aliases, orgConfig.Teams, "")
		logrus.Infof("Built local aliases for org %q from %d team(s): %d alias(es) available", orgName, len(orgConfig.Teams), len(aliases))
		for alias := range aliases {
			logrus.Debugf("  [%s] alias %q: %v", orgName, alias, sets.List(aliases.getMembers(alias)))
		}
	}

	// remove repos that should be skipped
	removeExcludedRepos(&orgConfig, o.skipRepos.Strings())

	// manage every repo defined in peribolos conf
	for orgName, orgConfig := range orgConfig.Orgs {
		logrus.Infof("Processing org %q with %d repo(s)", orgName, len(orgConfig.Repos))
		for repoName := range orgConfig.Repos {
			log := logrus.WithField("repo", orgName+"/"+repoName)
			log.Debug("Calculating alias changes")
			changes, changed := calculateAliasChanges(ghClient, cfg, orgName, repoName)
			if changes == nil || !changed {
				log.Info("No applicable changes, skipping")
				continue // skip change if not applicable
			}
			log.Infof("Found changes for %d alias(es)", len(changes))
			for alias, c := range changes {
				if c.add.Len() == 0 && c.remove.Len() == 0 {
					continue
				}
				log.Infof("  alias %q: add=%v remove=%v", alias, sets.List(c.add), sets.List(c.remove))
			}

			if !o.applyChanges {
				log.Info("Dry-run: skipping fork/commit/PR")
				continue
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
