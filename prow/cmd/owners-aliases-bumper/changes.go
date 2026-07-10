package main

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/repoowners"

	"github.com/gardener/gardener-landscape-kit/pkg/apis/config/v1alpha1"
	"github.com/gardener/gardener-landscape-kit/pkg/utils/meta"

	yaml4 "go.yaml.in/yaml/v4"
)

type change struct {
	add    sets.Set[string]
	remove sets.Set[string]
}

// fileGetter fetches a file from a repository. It is the subset of
// github.Client used by calculateAliasChanges.
type fileGetter interface {
	GetFile(org, repo, filepath, commit string) ([]byte, error)
}

func calculateAliasChanges(ghClient fileGetter, localAliases *fullOrgAliases, orgName, repoName string) (map[string]change, bool) {
	// get the aliases currently in the repo:
	raw, err := ghClient.GetFile(orgName, repoName, "OWNERS_ALIASES", "") // empty commit ID for latest commit on default branch
	var notFoundErr *github.FileNotFound

	if errors.As(err, &notFoundErr) {
		logrus.Infof("Repo %s/%s has no OWNER_ALIASES file skipping...", orgName, repoName)
		return nil, false // repo does not have a OWNERS_ALIASES file nothing to do
	}
	if err != nil {
		logrus.WithError(err).Errorf("Failed to get OWNER_ALIASES from %s/%s", orgName, repoName)
		return nil, false // skip gracefully if failed
	}

	aliases, err := repoowners.ParseAliasesConfig(raw)
	if err != nil {
		logrus.WithError(err).Errorf("Failed to parse OWNER_ALIASES from %s/%s", orgName, repoName)
		return nil, false // skip
	}

	changes := make(map[string]change)
	changed := false

	// compare all existing aliases with ours
	for alias, members := range aliases {
		localMembers := localAliases.getConfig(orgName).getMembers(alias)

		if localMembers == nil {
			continue // alias does not exist in our local config, skip
		}

		toBeAdded := localMembers.Difference(members)
		toBeDeleted := members.Difference(localMembers)
		if toBeAdded.Len() != 0 || toBeDeleted.Len() != 0 {
			changed = true
		}

		changes[alias] = change{
			add:    toBeAdded,
			remove: toBeDeleted,
		}
	}

	return changes, changed
}

// ownersAliasesFile models the OWNERS_ALIASES file. Only the string array
// syntax is accepted, not the {alias: {user1, user2}} notation (which is
// technically allowed by repoowners).
type ownersAliasesFile struct {
	Aliases map[string][]string `yaml:"aliases"`
}

func writeChanges(aliasesPath string, aliasChanges map[string]change) error {
	aliasOriginalRaw, err := os.ReadFile(aliasesPath)

	if err != nil {
		return fmt.Errorf("unable to read file %s: %w", aliasesPath, err)
	}

	var aliasOriginalParsed, aliasModified ownersAliasesFile

	// do it twice so we have 2 copies (could also do a deep of aliasOriginalParsed)
	if err := yaml4.Unmarshal(aliasOriginalRaw, &aliasOriginalParsed); err != nil {
		return fmt.Errorf("failed parsing file at %s: %w", aliasesPath, err)
	}
	aliasOriginalParsedRaw, err := yaml4.Marshal(aliasOriginalParsed)
	if err != nil {
		return fmt.Errorf("failed to parse original parsed yaml back to yaml... file: %s: %w", aliasesPath, err)
	}
	if err := yaml4.Unmarshal(aliasOriginalRaw, &aliasModified); err != nil {
		return fmt.Errorf("failed parsing file at %s: %w", aliasesPath, err)
	}

	// make modifications
	for alias, change := range aliasChanges {
		_, exists := aliasModified.Aliases[alias]
		if !exists {
			logrus.Warnf("expected %s to contain alias %s, however it was not found", aliasesPath, alias)
			continue
		}

		for user := range change.remove {
			aliasModified.Aliases[alias] = deleteValue(aliasModified.Aliases[alias], user)
		}

		for user := range change.add {
			aliasModified.Aliases[alias] = append(aliasModified.Aliases[alias], user)
		}
	}

	aliasModifiedParsedRaw, err := yaml4.Marshal(aliasModified)
	if err != nil {
		return fmt.Errorf("failed to parse modified aliases to yaml file: %s: %w", aliasesPath, err)
	}

	output, err := meta.ThreeWayMergeManifest(aliasOriginalParsedRaw, aliasModifiedParsedRaw, aliasOriginalRaw, v1alpha1.MergeModeHint)
	if err != nil {
		return fmt.Errorf("failed to merge new aliases with yaml file: %s: %w", aliasesPath, err)
	}

	fo, err := os.Create(aliasesPath)

	if err != nil {
		return fmt.Errorf("failed to open file %s for writing: %w", aliasesPath, err)
	}

	_, err = fo.Write(output)

	if err != nil {
		return fmt.Errorf("failed to write to file %s: %w", aliasesPath, err)
	}
	if err := fo.Close(); err != nil {
		return fmt.Errorf("failed closing file %s: %w", aliasesPath, err)
	}

	return nil
}

func deleteValue(arr []string, value string) []string {
	return slices.DeleteFunc(arr, func(e string) bool {
		return e == value
	})
}
