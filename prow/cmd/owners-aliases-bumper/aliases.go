package main

import (
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/prow/pkg/config/org"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/repoowners"
)

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
