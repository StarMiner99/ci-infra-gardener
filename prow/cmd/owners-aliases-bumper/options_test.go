// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/prow/pkg/flagutil"
)

var _ = Describe("Options", func() {
	// Only the fields parseArgs is responsible for are asserted. ghOpts is
	// populated with internal defaults by flagutil.GitHubOptions.AddFlags and
	// is not meaningfully comparable via a struct literal.
	DescribeTable("#parseArgs",
		func(args []string, expected *options) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			var actual options
			err := actual.parseArgs(flags, args)
			if expected == nil {
				Expect(err).To(HaveOccurred(), "expected error, got none")
				return
			}
			Expect(err).ToNot(HaveOccurred())
			Expect(actual.peribolosConfig).To(Equal(expected.peribolosConfig))
			Expect(actual.applyChanges).To(Equal(expected.applyChanges))
			Expect(actual.logLevel).To(Equal(expected.logLevel))
			Expect(actual.skipRepos.Strings()).To(Equal(expected.skipRepos.Strings()))
		},
		Entry("missing --peribolos-conf",
			[]string{},
			nil),
		Entry("bad --log-level",
			[]string{"--peribolos-conf=foo", "--log-level=nonsense"},
			nil),
		Entry("minimal",
			[]string{"--peribolos-conf=foo"},
			&options{
				peribolosConfig: "foo",
				skipRepos:       flagutil.NewStrings(),
				logLevel:        "info",
			}),
		Entry("confirm flag",
			[]string{"--peribolos-conf=foo", "--confirm"},
			&options{
				peribolosConfig: "foo",
				applyChanges:    true,
				skipRepos:       flagutil.NewStrings(),
				logLevel:        "info",
			}),
		Entry("skip repos",
			[]string{"--peribolos-conf=foo", "--skip-repos=org/a", "--skip-repos=org/b"},
			&options{
				peribolosConfig: "foo",
				skipRepos:       flagutil.NewStringsBeenSet("org/a", "org/b"),
				logLevel:        "info",
			}),
		Entry("debug log level",
			[]string{"--peribolos-conf=foo", "--log-level=debug"},
			&options{
				peribolosConfig: "foo",
				skipRepos:       flagutil.NewStrings(),
				logLevel:        "debug",
			}),
	)
})
