package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/prow/pkg/flagutil"
)

// TODO add option to change pr branch and maybe even if fork or not
type options struct {
	peribolosConfig string
	applyChanges    bool
	skipRepos       flagutil.Strings

	ghOpts flagutil.GitHubOptions

	logLevel string
}

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
		return errors.New("--peribolos-conf needs to be set")
	}

	return nil
}
