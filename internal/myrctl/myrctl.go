// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package myrctl implements the myrctl CLI for GitOps repository CI
// pipelines: `myrctl validate` and `myrctl render`.
//
// Exit codes: 0 ok, 1 validation errors, 2 usage/internal error.
//
// Validation uses exactly the code path of myriarchd (gitops.Validate), so a
// merge request that passes CI is accepted by myriarchd.
package myrctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/version"
)

// Exit codes of the CLI.
const (
	ExitOK         = 0
	ExitValidation = 1
	ExitUsage      = 2
)

// commitLabel is the revision name used for local directories.
const commitLabel = "local"

// ExitError carries the process exit code of a command. Its message (if any)
// is printed by Run; validation findings are already printed by the command
// itself, so their ExitError has no message.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

func usageErrorf(format string, a ...any) error {
	return &ExitError{Code: ExitUsage, Msg: fmt.Sprintf(format, a...)}
}

// ExitCode maps an error returned by a command to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitUsage // cobra usage errors (unknown flag/command, wrong args)
}

// Run executes myrctl with args (without the program name) and returns the
// exit code. Output goes to stdout/stderr.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	if msg := err.Error(); msg != "" {
		fmt.Fprintln(stderr, brand.CLIBinary+":", msg)
	}
	return ExitCode(err)
}

// NewRootCommand returns the root command.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   brand.CLIBinary,
		Short: "Validate and render myriarch GitOps repositories",
		Long: "myrctl validates and renders the GitOps repository of a myriarch installation, for example in CI\n" +
			"pipelines.\n\n" +
			"The short name myr is an alias that is not installed by default: alias myr=myrctl (POSIX shells)\n" +
			"or Set-Alias myr myrctl (PowerShell).",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newValidateCommand(), newRenderCommand(), &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Get().String())
		},
	})
	return root
}

// ----------------------------------------------------------------- shared ----

// repoDir validates that dir is an existing directory.
func repoDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		return usageErrorf("repository directory: %v", err)
	}
	if !st.IsDir() {
		return usageErrorf("repository directory: %s is not a directory", dir)
	}
	return nil
}

// loadComponents reads a component list file (format of
// examples/sim/components-*.json: {"components": {"receivers": {"otlp": {...}}}}).
func loadComponents(path string) (*domain.ComponentSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, usageErrorf("components file: %v", err)
	}
	var f struct {
		Components map[string]map[string]domain.ComponentDetails `json:"components"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, usageErrorf("components file %s: %v", path, err)
	}
	if len(f.Components) == 0 {
		return nil, usageErrorf("components file %s: no \"components\" found", path)
	}
	return &domain.ComponentSet{Components: f.Components}, nil
}

func checkOutput(out string, allowed ...string) error {
	for _, a := range allowed {
		if out == a {
			return nil
		}
	}
	return usageErrorf("unknown --output %q (allowed: %s)", out, strings.Join(allowed, ", "))
}

func countIssues(is domain.Issues) (errs, warns int) {
	for _, i := range is {
		if i.Severity == domain.SeverityError {
			errs++
		} else {
			warns++
		}
	}
	return
}

func formatIssue(is domain.Issue) string {
	var b strings.Builder
	switch {
	case is.File != "" && is.Line > 0:
		fmt.Fprintf(&b, "%s:%d: ", is.File, is.Line)
	case is.File != "":
		fmt.Fprintf(&b, "%s: ", is.File)
	}
	fmt.Fprintf(&b, "%s %s: %s", is.Severity, is.Code, is.Message)
	var extra []string
	if is.Object != "" {
		extra = append(extra, is.Object)
	}
	if is.Path != "" {
		extra = append(extra, is.Path)
	}
	if len(extra) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(extra, ", "))
	}
	return b.String()
}

func writeIssues(w io.Writer, issues domain.Issues) {
	for _, is := range issues {
		fmt.Fprintln(w, formatIssue(is))
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// -------------------------------------------------------------- validate ----

// validateReport is the JSON output of `myrctl validate --output json`.
type validateReport struct {
	Valid    bool           `json:"valid"`
	Errors   int            `json:"errors"`
	Warnings int            `json:"warnings"`
	Objects  map[string]int `json:"objects"`
	Samples  int            `json:"samples"`
	Issues   domain.Issues  `json:"issues"`
}

func snapshotCounts(s *domain.GitOpsSnapshot) map[string]int {
	if s == nil {
		return map[string]int{}
	}
	return map[string]int{
		"Blueprint":     len(s.Blueprints),
		"Fleet":         len(s.Fleets),
		"RolloutPolicy": len(s.RolloutPolicies),
		"Executor":      len(s.Executors),
		"Inventory":     len(s.Inventories),
	}
}

func newValidateCommand() *cobra.Command {
	var (
		componentsFile string
		output         string
		strict         bool
	)
	cmd := &cobra.Command{
		Use:   "validate [dir]",
		Short: "Validate all objects of a GitOps repository (default dir: .)",
		Long: "Validates the repository exactly like myriarchd does before it applies a revision and\n" +
			"renders every fleet for the matching sample agents (samples/agents/*.yaml).\n\n" +
			"Exit codes: 0 valid (warnings allowed), 1 validation errors, 2 usage or internal error.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			if err := checkOutput(output, "text", "json"); err != nil {
				return err
			}
			if err := repoDir(dir); err != nil {
				return err
			}
			var comps *domain.ComponentSet
			if componentsFile != "" {
				var err error
				if comps, err = loadComponents(componentsFile); err != nil {
					return err
				}
			}
			res := gitops.Validate(os.DirFS(dir), ".", commitLabel, blueprint.NewRenderer(blueprint.Options{}), comps)
			if res.Issues == nil {
				res.Issues = domain.Issues{}
			}
			errs, warns := countIssues(res.Issues)
			failed := errs > 0 || (strict && warns > 0)
			counts := snapshotCounts(res.Snapshot)

			out := cmd.OutOrStdout()
			if output == "json" {
				if err := writeJSON(out, validateReport{
					Valid: !failed, Errors: errs, Warnings: warns, Objects: counts, Samples: len(res.Samples), Issues: res.Issues,
				}); err != nil {
					return usageErrorf("write output: %v", err)
				}
			} else {
				writeIssues(out, res.Issues)
				kinds := make([]string, 0, len(counts))
				for k := range counts {
					kinds = append(kinds, k)
				}
				sort.Strings(kinds)
				parts := make([]string, 0, len(kinds))
				for _, k := range kinds {
					parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
				}
				status := "OK"
				if failed {
					status = "FAILED"
				}
				fmt.Fprintf(out, "%s: %s; %d samples; %d errors, %d warnings\n", status, strings.Join(parts, ", "), len(res.Samples), errs, warns)
			}
			if failed {
				return &ExitError{Code: ExitValidation}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&componentsFile, "components", "", "component list (JSON) enabling the unknown_component check for all sample renderings")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors (exit 1)")
	return cmd
}

// ---------------------------------------------------------------- render ----

type renderOutput struct {
	Fleet            string            `json:"fleet"`
	Blueprint        string            `json:"blueprint"`
	Layers           []string          `json:"layers"`
	Parameters       map[string]any    `json:"parameters"`
	ParameterSources map[string]string `json:"parameterSources"`
	Hash             string            `json:"hash"`
	Config           string            `json:"config"`
	Issues           domain.Issues     `json:"issues"`
}

// splitKV parses key=value flag values.
func splitKV(flag string, kvs []string) (map[string]string, error) {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, usageErrorf("--%s %q: expected key=value", flag, kv)
		}
		m[strings.TrimSpace(k)] = v
	}
	return m, nil
}

func newRenderCommand() *cobra.Command {
	var (
		dir            string
		fleetName      string
		sampleName     string
		attrs          []string
		labels         []string
		params         []string
		componentsFile string
		output         string
	)
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render the collector config of a fleet for a (sample) agent",
		Long: "Renders the collector configuration for a sample agent (--sample) or for attributes given\n" +
			"on the command line (--attr key=value, repeatable). Without --fleet the fleet is chosen like\n" +
			"myriarchd does (selectors and priority). Inventory entries of the repository are applied.\n\n" +
			"The configuration is printed to stdout (--output yaml) or as a JSON document with parameters,\n" +
			"hash and issues (--output json). Exit codes: 0 ok, 1 render errors, 2 usage or internal error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output, "yaml", "json"); err != nil {
				return err
			}
			if sampleName == "" && len(attrs) == 0 && fleetName == "" {
				return usageErrorf("give --fleet NAME and/or --sample NAME (or --attr key=value ...)")
			}
			if sampleName != "" && len(attrs) > 0 {
				return usageErrorf("--sample and --attr are mutually exclusive")
			}
			if err := repoDir(dir); err != nil {
				return err
			}
			attrMap, err := splitKV("attr", attrs)
			if err != nil {
				return err
			}
			labelMap, err := splitKV("label", labels)
			if err != nil {
				return err
			}
			paramMap, err := splitKV("param", params)
			if err != nil {
				return err
			}
			overrides := map[string]any{}
			for k, v := range paramMap {
				var val any
				if err := yaml.Unmarshal([]byte(v), &val); err != nil {
					return usageErrorf("--param %s: %v", k, err)
				}
				overrides[k] = val
			}
			var comps *domain.ComponentSet
			if componentsFile != "" {
				if comps, err = loadComponents(componentsFile); err != nil {
					return err
				}
			}

			r := blueprint.NewRenderer(blueprint.Options{})
			res := gitops.Validate(os.DirFS(dir), ".", commitLabel, r, nil)
			if res.Snapshot == nil {
				return usageErrorf("cannot read repository %s", dir)
			}
			// Repository findings that are not about the render itself go to
			// stderr; `myrctl validate` is the tool to fix them.
			if errs, _ := countIssues(res.Issues); errs > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: the repository has %d validation errors (run `myrctl validate`)\n", errs)
			}

			if sampleName != "" {
				found := false
				for _, s := range res.Samples {
					if s.Name == sampleName {
						attrMap, labelMap, found = s.Attributes, s.Labels, true
						break
					}
				}
				if !found {
					return usageErrorf("sample %q not found in samples/agents (have: %s)", sampleName, sampleNames(res.Samples))
				}
			}

			inv := blueprint.ApplyInventory(res.Snapshot, attrMap)
			merged := map[string]string{}
			for k, v := range labelMap {
				merged[k] = v
			}
			for k, v := range inv.Labels { // inventory labels win over server labels
				merged[k] = v
			}
			view := blueprint.ViewFrom(attrMap, merged)

			var fleet *domain.Fleet
			if fleetName != "" {
				for i := range res.Snapshot.Fleets {
					if res.Snapshot.Fleets[i].Name == fleetName {
						fleet = &res.Snapshot.Fleets[i]
					}
				}
				if fleet == nil {
					return usageErrorf("fleet %q not found", fleetName)
				}
			} else {
				m := blueprint.MatchFleet(res.Snapshot, view)
				if m.Fleet == nil {
					if m.Error != "" {
						return usageErrorf("fleet assignment: %s", m.Error)
					}
					return usageErrorf("the agent matches no fleet (use --fleet)")
				}
				fleet = m.Fleet
			}

			rr, err := r.Render(blueprint.RenderInput{
				Snapshot: res.Snapshot, Fleet: fleet, Agent: blueprint.AgentInput{Attributes: view},
				InventoryParameters: inv.Parameters, Overrides: overrides, Components: comps,
			})
			if err != nil {
				return &ExitError{Code: ExitUsage, Msg: fmt.Sprintf("render: %v", err)}
			}
			if rr.Issues == nil {
				rr.Issues = domain.Issues{}
			}
			errs, _ := countIssues(rr.Issues)
			out := cmd.OutOrStdout()
			if output == "json" {
				if err := writeJSON(out, renderOutput{
					Fleet: fleet.Name, Blueprint: rr.Blueprint, Layers: rr.Layers, Parameters: rr.Parameters,
					ParameterSources: rr.ParameterSources, Hash: rr.Hash.String(), Config: string(rr.Body), Issues: rr.Issues,
				}); err != nil {
					return usageErrorf("write output: %v", err)
				}
			} else {
				if errs == 0 {
					fmt.Fprint(out, string(rr.Body))
				}
				writeIssues(cmd.ErrOrStderr(), rr.Issues)
			}
			if errs > 0 {
				return &ExitError{Code: ExitValidation}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", ".", "GitOps repository directory")
	f.StringVar(&fleetName, "fleet", "", "fleet name (default: the fleet the agent is assigned to)")
	f.StringVar(&sampleName, "sample", "", "sample agent name (samples/agents/*.yaml)")
	f.StringArrayVar(&attrs, "attr", nil, "agent attribute key=value (repeatable; alternative to --sample)")
	f.StringArrayVar(&labels, "label", nil, "server label key=value (repeatable)")
	f.StringArrayVar(&params, "param", nil, "parameter override key=value (repeatable, YAML scalar)")
	f.StringVar(&componentsFile, "components", "", "component list (JSON) enabling the unknown_component check")
	f.StringVarP(&output, "output", "o", "yaml", "output format: yaml or json")
	return cmd
}

func sampleNames(samples []blueprint.Sample) string {
	names := make([]string, 0, len(samples))
	for _, s := range samples {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
