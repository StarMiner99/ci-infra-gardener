package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/prow/pkg/config/org"
	"sigs.k8s.io/prow/pkg/flagutil"
	"sigs.k8s.io/prow/pkg/git/v2"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/logrusutil"
	"sigs.k8s.io/prow/pkg/repoowners"
	"sigs.k8s.io/yaml"

	"github.com/gardener/gardener-landscape-kit/pkg/apis/config/v1alpha1"
	"github.com/gardener/gardener-landscape-kit/pkg/utils/meta"

	yaml4 "go.yaml.in/yaml/v4"
)

// TODO add option to change pr branch and maybe even if fork or not
type options struct {
	peribolosConfig string
	applyChanges    bool
	skipRepos       flagutil.Strings

	ghOpts flagutil.GitHubOptions

	logLevel string
}

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

func parseOptions() options {
	var o options
	if err := o.parseArgs(flag.CommandLine, os.Args[1:]); err != nil {
		logrus.Fatalf("Invalid flags: %v", err)
	}
	return o
}

func (o *options) parseArgs(flags *flag.FlagSet, args []string) error {
	flags.StringVar(&o.peribolosConfig, "peribolos-conf", "", "The path to the Peribolos config, will be used to populate aliases which have the same name as a GitHub team.")
	o.skipRepos = flagutil.NewStrings()
	flags.Var(&o.skipRepos, "skip-repos", "By default all repos defined in the Peribolos config will be managed, list all repos that should be skipped here. (in format: <org>/<repo>)")
	flags.BoolVar(&o.applyChanges, "confirm", false, "Set this flag in Order to apply the changes, without this flag only information on what would be changed is printed.")
	o.ghOpts.AddFlags(flags)

	// logrus
	flags.StringVar(&o.logLevel, "log-level", logrus.InfoLevel.String(), fmt.Sprintf("Logging level, one of %v", logrus.AllLevels))

	if err := flags.Parse(args); err != nil {
		return err
	}

	// logrus
	level, err := logrus.ParseLevel(o.logLevel)
	if err != nil {
		return fmt.Errorf("--log-level invalid: %w", err)
	}
	logrus.SetLevel(level)
	logrus.SetReportCaller(level >= logrus.DebugLevel)

	// check if args provide valid configuration
	if o.peribolosConfig == "" {
		return errors.New("--peribolos-conf needs to be set.")
	}

	return nil
}

func main() {
	logrusutil.ComponentInit()

	o := parseOptions()

	cfg := NewFullOrgAliases()

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
		addMembersFromTeams(cfg.GetConfig(orgName), orgConfig.Teams, "")
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
			if err := commitAndPush(ghClient, repoClient, orgName, repoName); err != nil {
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

	//for orgName, orgConfig := range orgConfig.Orgs {
	//}
}

func parseOrgConfig(path string) org.FullConfig {
	raw, err := os.ReadFile(path)
	if err != nil {
		logrus.WithError(err).Fatal("Could not read --peribolos-conf file")
	}

	var cfg org.FullConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		logrus.WithError(err).Fatal("Failed to parse peribolos configuration")
	}
	return cfg
}

/*
func parseOwnerAlias(path string) repoowners.RepoAliases {
	raw, err := os.ReadFile(path)
	if err != nil {
		logrus.WithError(err).Fatal("Could not read --owner-alias-conf file")
	}

	aliases, err := repoowners.ParseAliasesConfig(raw)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to load OWNERS_ALIASES")
	}

	return aliases
}
*/

// holds alias -> member map per Org
type OrgAliases repoowners.RepoAliases

func (orgAliases OrgAliases) addMember(aliasName string, member string) {
	aliasMembers, exists := orgAliases[aliasName]

	if !exists {
		aliasMembers = sets.New(member)
		orgAliases[aliasName] = aliasMembers
		return
	}

	aliasMembers.Insert(member)
}

func (orgAliases OrgAliases) getMembers(aliasName string) sets.Set[string] {
	aliasMembers, exists := orgAliases[aliasName]

	if !exists {
		return nil // return nil if this alias does not exist
	}

	return aliasMembers
}

// aliases mapped per org
type FullOrgAliases struct {
	aliases map[string]OrgAliases // OrgAliases is map
}

func NewFullOrgAliases() *FullOrgAliases {
	return &FullOrgAliases{
		aliases: make(map[string]OrgAliases),
	}
}

func (aliases *FullOrgAliases) GetConfig(org string) OrgAliases {
	orgAliases, exists := aliases.aliases[org]

	if !exists {
		orgAliases = make(OrgAliases)
		aliases.aliases[org] = orgAliases
	}

	return orgAliases
}

func addMembersFromTeams(aliases OrgAliases, teams map[string]org.Team, aliasPrefix string) {
	for teamName, team := range teams {
		alias := github.NormLogin(aliasPrefix + teamName)
		for _, member := range team.Members {
			aliases.addMember(alias, github.NormLogin(member))
		}
		for _, maintainer := range team.Maintainers {
			aliases.addMember(alias, github.NormLogin(maintainer))
		}
		// handle children recursively (children teams could have same name as any parent team -> aliasPrefix with '/' delimiter)
		addMembersFromTeams(aliases, team.Children, aliasPrefix+teamName+"/")
	}
}

func removeExcludedRepos(orgs *org.FullConfig, excludedRepos []string) {
	for _, excludedRepo := range excludedRepos {
		splitRepoString := strings.Split(excludedRepo, "/")

		if len(splitRepoString) != 2 {
			logrus.Fatalf("Excluded repository %s is not formatted correctly, please define it as: <org>/<repo name>", excludedRepo)
		}

		// warn the user if the config does not look right
		if _, exists := orgs.Orgs[splitRepoString[0]]; !exists {
			logrus.Warnf("Attempted to exclude %s however, organization %s is not defined in the Peribolos config, excluding it anyways!", excludedRepo, splitRepoString[0])
		} else {
			if _, exists := orgs.Orgs[splitRepoString[0]].Repos[splitRepoString[1]]; !exists {
				logrus.Warnf("Attempted to exclude %s however, %s is not defined in the Peribolos config, excluding it anyways!", excludedRepo, splitRepoString[1])
			}
		}

		// remove repo from our config
		delete(orgs.Orgs[splitRepoString[0]].Repos, splitRepoString[1])
	}
}

/*
// RepoAliases uses a Set instead of an array, this could cause issues when committing, messing up the order of entries
// therefore we have our own version which does not have this issue
type OrderedAliases map[string][]string

func parseOrderedAliasesConfig(b []byte) (OrderedAliases, error){
	result := make(OrderedAliases)

	config := &struct {
		Data map[string]any `json:"aliases,omitempty"`
	}{}
	if err := yaml.Unmarshal(b, config); err != nil {
		return result, err
	}

	for alias, expanded := range config.Data {
		switch v := expanded.(type) {
		case []any:
			// Convert []interface{} to []string
			var members []string
			for _, member := range v {
				memberAsString, ok := member.(string)
				if !ok {
					return result, fmt.Errorf("unexpected type for alias group member: %T", member)
				}
				members = append(members, memberAsString)
			}
			result[github.NormLogin(alias)] = NormLogins(members)
		case string:
			// Alias group must contain a list of members.
			return result, fmt.Errorf("alias group '%s' must contain a list of members", alias)
		case map[string]any:
			// Handle Flow Style Mapping (Inline Dictionary/Object Syntax). Example - aliases: { alias-group: { alias1, alias2 } }
			var members []string
			for key := range v {
				members = append(members, key)
			}
			result[github.NormLogin(alias)] = NormLogins(members)
		case nil:
			// Handle empty alias group as an empty list. Examples - aliases: { alias-group: }
			result[github.NormLogin(alias)] = []string{}
		default:
			return result, fmt.Errorf("unexpected type for alias group: %T", v)
		}
	}
	return result, nil
}

func NormLogins(logins []string) []string {
	normedLogins := make([]string, len(logins))
	for _, login := range logins {
		normedLogins = append(normedLogins, github.NormLogin(login))
	}
	return normedLogins
}
*/

type change struct {
	add    sets.Set[string]
	remove sets.Set[string]
}

func calculateAliasChanges(ghClient github.Client, localAliases *FullOrgAliases, orgName, repoName string) (map[string]change, bool) {
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
		localMembers := localAliases.GetConfig(orgName).getMembers(alias)

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
	/*


		name, err := ghClient.CreateFork(orgName, repoName)



		if err != nil {
			logrus.WithError(err).Fatalf("Error creating fork for: %s/%s", orgName, repoName)
		}

		// how can we make a write operation ? either through GH api but no methods exposed by ghclient sadly
		// other option would be using bumper.call and cloning the repo, making the edits and pushing it
		// either way a fork is required which is.. not great, maybe create branch instead of forking?

		stdout := bumper.HideSecretsWriter{Delegate: os.Stdout, Censor: secret.Censor}
		stderr := bumper.HideSecretsWriter{Delegate: os.Stderr, Censor: secret.Censor}
		bumper.Call(stdout, stderr, gitCmd, []string{"clone" })
	*/

}

// only accept string array syntax no {alias: {user1, user2}} notation (technically allowed in repoowners)
type OwnerAliasFileYAMLStruct struct {
	Aliases map[string][]string `yaml:"aliases"`
}

func writeChanges(aliasesPath string, aliasChanges map[string]change) error {
	aliasOriginalRaw, err := os.ReadFile(aliasesPath)

	if err != nil {
		return errors.Join(err, fmt.Errorf("Unable to read file %s", aliasesPath))
	}

	var aliasOriginalParsed, aliasModified OwnerAliasFileYAMLStruct

	// do it twice so we have 2 copies (could also do a deep of aliasOriginalParsed)
	if err := yaml4.Unmarshal(aliasOriginalRaw, &aliasOriginalParsed); err != nil {
		return errors.Join(err, fmt.Errorf("Failed parsing file at %s", aliasesPath))
	}
	aliasOriginalParsedRaw, err := yaml4.Marshal(aliasOriginalParsed)
	if err != nil {
		return errors.Join(err, fmt.Errorf("Failed to parse original parsed yaml back to yaml... file: %s", aliasesPath))
	}
	if err := yaml4.Unmarshal(aliasOriginalRaw, &aliasModified); err != nil {
		return errors.Join(err, fmt.Errorf("Failed parsing file at %s", aliasesPath))
	}

	// make modifications
	for alias, change := range aliasChanges {
		_, exists := aliasModified.Aliases[alias]
		if !exists {
			logrus.Warnf("Expected %s to contain alias %s, however it was not found!", aliasesPath, alias)
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
		return errors.Join(err, fmt.Errorf("Failed to parse modified aliases to yaml. file: %s", aliasesPath))
	}

	output, err := meta.ThreeWayMergeManifest(aliasOriginalParsedRaw, aliasModifiedParsedRaw, aliasOriginalRaw, v1alpha1.MergeModeHint)
	if err != nil {
		return errors.Join(err, fmt.Errorf("Failed to merge new aliases with yaml file: %s", aliasesPath))
	}

	fo, err := os.Create(aliasesPath)

	if err != nil {
		return errors.Join(err, fmt.Errorf("Failed to open file %s for writing", aliasesPath))
	}

	_, err = fo.Write(output)

	if err != nil {
		return errors.Join(err, fmt.Errorf("Failed to write to file %s", aliasesPath))
	}
	if err := fo.Close(); err != nil {
		return errors.Join(err, fmt.Errorf("Failed closing file: %s", aliasesPath))
	}

	return nil
}

func deleteValue(arr []string, value string) []string {
	return slices.DeleteFunc(arr, func(e string) bool {
		return e == value
	})
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
