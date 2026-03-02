package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/matgreaves/rig/internal/explain"
)

const artifactName = "rig-artifacts"

// known subcommands that ci can delegate to.
var ciSubcommands = map[string]bool{
	"ls": true, "explain": true, "traffic": true, "logs": true,
}

func runCi(args []string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found — install it from https://cli.github.com")
	}

	target, subcmd, subArgs, flags := parseCiArgs(args)

	// Resolve target to a run ID.
	runID, err := resolveRunID(target)
	if err != nil {
		return err
	}

	// Download/cache artifacts.
	dir, err := ensureArtifacts(runID)
	if err != nil {
		return err
	}

	// Locate log directory within the artifact cache.
	ciLogDir := filepath.Join(dir, ".rig", "logs")
	if _, err := os.Stat(ciLogDir); err != nil {
		return fmt.Errorf("no .rig/logs/ found in artifacts for run %d", runID)
	}

	if subcmd != "" {
		return dispatchSubcommand(subcmd, subArgs, ciLogDir)
	}

	return runCiSummary(runID, ciLogDir, flags)
}

// ciFlags holds parsed flags for summary mode.
type ciFlags struct {
	failed  bool
	passed  bool
	pretty  bool
	verbose bool
}

// parseCiArgs parses: [target] [flags] [subcommand] [subcommand-args...]
//
// The first non-flag arg that matches a known subcommand splits the args:
// everything before it is ci args (target + summary flags), everything after
// is forwarded to the subcommand. This matches the git/docker convention
// where parent flags come before the subcommand.
func parseCiArgs(args []string) (target, subcmd string, subArgs []string, flags ciFlags) {
	// Find the subcommand position: first non-flag arg that is a known subcommand.
	subcmdIdx := -1
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if ciSubcommands[a] {
			subcmdIdx = i
			break
		}
	}

	// Split into ci args and subcommand args.
	var ciArgs []string
	if subcmdIdx >= 0 {
		subcmd = args[subcmdIdx]
		ciArgs = args[:subcmdIdx]
		subArgs = args[subcmdIdx+1:]
	} else {
		ciArgs = args
	}

	// Intercept help in ci args only — --help after a subcommand is forwarded.
	for _, a := range ciArgs {
		if a == "--help" || a == "-h" {
			printCiUsage()
			os.Exit(0)
		}
	}

	// Parse ci args: optional target + summary flags.
	for _, a := range ciArgs {
		switch a {
		case "--failed":
			flags.failed = true
		case "--passed":
			flags.passed = true
		case "-p":
			flags.pretty = true
		case "-v":
			flags.verbose = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "rig ci: unknown flag %q\n\n", a)
				printCiUsage()
				os.Exit(1)
			}
			if target != "" {
				fmt.Fprintf(os.Stderr, "rig ci: unexpected argument %q\n\n", a)
				printCiUsage()
				os.Exit(1)
			}
			target = a
		}
	}
	return
}

func printCiUsage() {
	fmt.Fprintf(os.Stderr, `Usage: rig ci [target] [flags] [subcommand] [args...]

Analyze CI run artifacts. Downloads rig-artifacts from GitHub Actions,
caches them locally, and either shows a run summary or delegates to
existing CLI commands (ls, explain, traffic, logs).

Target (optional — defaults to current branch's latest run):
  (empty)       latest run on current git branch
  74            PR number (1–6 digits)
  12345678      run ID (7+ digits)

Flags (summary mode — no subcommand):
  --failed      only show failed/crashed tests
  --passed      only show passed tests
  -p            pretty-print (default is JSON)
  -v            verbose — full explain output per failed test (requires -p)

Subcommands (delegated with RIG_LOGS set to cached artifacts):
  ls            list tests in the run
  explain       analyze a specific test failure
  traffic       inspect captured traffic
  logs          view service logs

Examples:
  rig ci                          # summary of current branch (JSON)
  rig ci -p                       # summary of current branch (pretty)
  rig ci 74 -p                    # summary of PR #74
  rig ci 74 --failed -p -v        # verbose failure details for PR #74
  rig ci 74 explain S3 -p         # drill into a specific test
  rig ci traffic S3               # traffic for current branch
  rig ci 74 ls --failed           # list failures
`)
}

func dispatchSubcommand(subcmd string, args []string, ciLogDir string) error {
	os.Setenv("RIG_LOGS", ciLogDir)
	switch subcmd {
	case "ls":
		return runLs(args)
	case "explain":
		return runExplain(args)
	case "traffic":
		return runTraffic(args)
	case "logs":
		return runLogs(args)
	default:
		return fmt.Errorf("unknown subcommand %q", subcmd)
	}
}

// --- Target resolution ---

func resolveRunID(target string) (int64, error) {
	if target == "" {
		return resolveRunFromBranch("")
	}
	n, err := strconv.ParseInt(target, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid target %q — expected PR number, run ID, or empty for current branch", target)
	}
	if n <= 999999 {
		return resolveRunFromPR(n)
	}
	return validateRunID(n)
}

func resolveRunFromBranch(branch string) (int64, error) {
	if branch == "" {
		out, err := execOutput("git", "branch", "--show-current")
		if err != nil {
			return 0, fmt.Errorf("cannot determine current branch: %w", err)
		}
		branch = strings.TrimSpace(out)
		if branch == "" {
			return 0, fmt.Errorf("detached HEAD — specify a PR number or run ID")
		}
	}

	raw, err := ghJSON("run", "list", "--branch", branch, "--limit", "1", "--json", "databaseId")
	if err != nil {
		return 0, fmt.Errorf("gh run list: %w", err)
	}
	var runs []struct {
		DatabaseId int64 `json:"databaseId"`
	}
	if err := json.Unmarshal(raw, &runs); err != nil {
		return 0, fmt.Errorf("parse run list: %w", err)
	}
	if len(runs) == 0 {
		return 0, fmt.Errorf("no CI runs found for branch %q", branch)
	}
	return runs[0].DatabaseId, nil
}

func resolveRunFromPR(pr int64) (int64, error) {
	raw, err := ghJSON("pr", "view", strconv.FormatInt(pr, 10), "--json", "headRefName")
	if err != nil {
		return 0, fmt.Errorf("gh pr view: %w", err)
	}
	var prInfo struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(raw, &prInfo); err != nil {
		return 0, fmt.Errorf("parse PR info: %w", err)
	}
	if prInfo.HeadRefName == "" {
		return 0, fmt.Errorf("PR #%d has no head branch", pr)
	}
	return resolveRunFromBranch(prInfo.HeadRefName)
}

func validateRunID(id int64) (int64, error) {
	raw, err := ghJSON("run", "view", strconv.FormatInt(id, 10), "--json", "databaseId")
	if err != nil {
		return 0, fmt.Errorf("gh run view: %w", err)
	}
	var run struct {
		DatabaseId int64 `json:"databaseId"`
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return 0, fmt.Errorf("parse run info: %w", err)
	}
	return run.DatabaseId, nil
}

// --- Artifact download & cache ---

func ensureArtifacts(runID int64) (string, error) {
	owner, err := repoIdentifier()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(defaultRigDir(), "ci", owner, strconv.FormatInt(runID, 10))

	// Check cache.
	if _, err := os.Stat(filepath.Join(dir, ".rig", "logs")); err == nil {
		return dir, nil
	}

	// Download.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	_, err = execOutput("gh", "run", "download", strconv.FormatInt(runID, 10),
		"--name", artifactName, "--dir", dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("download %q artifact for run %d: %w\n\nEnsure your CI workflow uploads rig artifacts:\n\n  - uses: actions/upload-artifact@v4\n    if: always()\n    with:\n      name: rig-artifacts\n      path: |\n        .rig/logs/\n        .rig/tmp/", artifactName, runID, err)
	}
	return dir, nil
}

func repoIdentifier() (string, error) {
	raw, err := ghJSON("repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	var repo struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(raw, &repo); err != nil {
		return "", fmt.Errorf("parse repo info: %w", err)
	}
	return repo.NameWithOwner, nil
}

// --- Run info ---

type ciRunInfo struct {
	ID         int64
	Branch     string
	Conclusion string
	URL        string
	Duration   time.Duration
}

func fetchRunInfo(runID int64) (*ciRunInfo, error) {
	raw, err := ghJSON("run", "view", strconv.FormatInt(runID, 10),
		"--json", "databaseId,headBranch,conclusion,url,updatedAt,createdAt")
	if err != nil {
		return nil, fmt.Errorf("gh run view: %w", err)
	}
	var data struct {
		DatabaseId int64     `json:"databaseId"`
		HeadBranch string    `json:"headBranch"`
		Conclusion string    `json:"conclusion"`
		URL        string    `json:"url"`
		CreatedAt  time.Time `json:"createdAt"`
		UpdatedAt  time.Time `json:"updatedAt"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse run info: %w", err)
	}
	return &ciRunInfo{
		ID:         data.DatabaseId,
		Branch:     data.HeadBranch,
		Conclusion: data.Conclusion,
		URL:        data.URL,
		Duration:   data.UpdatedAt.Sub(data.CreatedAt),
	}, nil
}

// --- Summary ---

type ciSummaryJSON struct {
	Run       ciRunJSON        `json:"run"`
	Summary   ciSummaryCount   `json:"summary"`
	Tests     []ciTestJSON     `json:"tests"`
	Artifacts []ciArtifactJSON `json:"artifacts"`
}

type ciRunJSON struct {
	ID         int64  `json:"id"`
	Branch     string `json:"branch"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	DurationS  int    `json:"duration_s"`
}

type ciSummaryCount struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Crashed int `json:"crashed"`
}

type ciTestJSON struct {
	Test            string                   `json:"test"`
	Outcome         string                   `json:"outcome"`
	DurationMs      float64                  `json:"duration_ms"`
	Services        []string                 `json:"services"`
	Assertions      []explain.Assertion      `json:"assertions,omitempty"`
	Errors          []explain.TrafficError   `json:"errors,omitempty"`
	ServiceFailures []explain.ServiceFailure `json:"service_failures,omitempty"`
	ServiceErrors   []explain.ServiceError   `json:"service_errors,omitempty"`
	Stall           *explain.StallInfo       `json:"stall,omitempty"`
}

type ciArtifactJSON struct {
	Key        string  `json:"key"`
	DurationMs float64 `json:"duration_ms,omitempty"`
	Cached     bool    `json:"cached,omitempty"`
}

func runCiSummary(runID int64, ciLogDir string, flags ciFlags) error {
	info, err := fetchRunInfo(runID)
	if err != nil {
		return err
	}

	paths, err := scanDir(ciLogDir, "")
	if err != nil {
		return fmt.Errorf("read CI log directory: %w", err)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no log files found in CI artifacts for run %d", runID)
	}

	// Analyze all tests.
	var tests []ciTestJSON
	var artifacts []ciArtifactJSON
	artifactSeen := map[string]bool{}

	for _, path := range paths {
		report, err := explain.AnalyzeFile(path)
		if err != nil {
			continue
		}

		test := ciTestJSON{
			Test:       report.Test,
			Outcome:    report.Outcome,
			DurationMs: report.DurationMs,
			Services:   report.Services,
		}
		if report.Outcome != "passed" {
			test.Assertions = report.Assertions
			test.Errors = report.Errors
			test.ServiceFailures = report.ServiceFailures
			test.ServiceErrors = report.ServiceErrors
			test.Stall = report.Stall
		}
		tests = append(tests, test)

		fileArtifacts := scanArtifactEvents(path)
		for _, a := range fileArtifacts {
			if !artifactSeen[a.Key] {
				artifactSeen[a.Key] = true
				artifacts = append(artifacts, a)
			}
		}
	}

	// Compute summary from ALL tests before filtering.
	summary := ciSummaryCount{Total: len(tests)}
	for _, t := range tests {
		switch t.Outcome {
		case "passed":
			summary.Passed++
		case "failed":
			summary.Failed++
		case "crashed":
			summary.Crashed++
		}
	}

	// Apply outcome filter to the displayed test list.
	if flags.failed || flags.passed {
		var filtered []ciTestJSON
		for _, t := range tests {
			if flags.failed && (t.Outcome == "failed" || t.Outcome == "crashed") {
				filtered = append(filtered, t)
			} else if flags.passed && t.Outcome == "passed" {
				filtered = append(filtered, t)
			}
		}
		tests = filtered
	}

	if flags.pretty {
		renderCiPretty(os.Stdout, info, summary, tests, artifacts, flags.verbose)
	} else {
		renderCiJSON(os.Stdout, info, summary, tests, artifacts)
	}
	return nil
}

func renderCiJSON(w io.Writer, info *ciRunInfo, summary ciSummaryCount, tests []ciTestJSON, artifacts []ciArtifactJSON) {
	out := ciSummaryJSON{
		Run: ciRunJSON{
			ID:         info.ID,
			Branch:     info.Branch,
			Conclusion: info.Conclusion,
			URL:        info.URL,
			DurationS:  int(info.Duration.Seconds()),
		},
		Summary:   summary,
		Tests:     tests,
		Artifacts: artifacts,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

func renderCiPretty(w io.Writer, info *ciRunInfo, summary ciSummaryCount, tests []ciTestJSON, artifacts []ciArtifactJSON, verbose bool) {
	// Header.
	durStr := formatRunDuration(info.Duration)
	fmt.Fprintf(w, "%s  %s  %s  %s\n",
		bold(fmt.Sprintf("Run #%d", info.ID)),
		info.Branch,
		colorOutcome(info.Conclusion),
		durStr)
	fmt.Fprintln(w, info.URL)
	fmt.Fprintln(w)

	// Test table.
	headers := []string{"OUTCOME", "NAME", "DURATION"}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}

	type row struct {
		cols [3]string
	}
	rows := make([]row, len(tests))
	for i, t := range tests {
		outcome := t.Outcome
		if outcome == "" {
			outcome = "unknown"
		}
		durStr := formatLsDuration(t.DurationMs)
		rows[i] = row{cols: [3]string{outcome, t.Test, durStr}}
		for j, c := range rows[i].cols {
			if len(c) > widths[j] {
				widths[j] = len(c)
			}
		}
	}

	for i, h := range headers {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprintf(w, "%-*s", widths[i], bold(h))
	}
	fmt.Fprintln(w)

	for _, r := range rows {
		for i, c := range r.cols {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			padded := fmt.Sprintf("%-*s", widths[i], c)
			if i == 0 {
				fmt.Fprint(w, colorOutcome(padded))
			} else {
				fmt.Fprint(w, padded)
			}
		}
		fmt.Fprintln(w)
	}

	// Summary line — always reflects the full run, not filtered view.
	fmt.Fprintln(w)
	parts := []string{fmt.Sprintf("%d tests:", summary.Total)}
	if summary.Passed > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", summary.Passed))
	}
	if summary.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", summary.Failed))
	}
	if summary.Crashed > 0 {
		parts = append(parts, fmt.Sprintf("%d crashed", summary.Crashed))
	}
	fmt.Fprintln(w, strings.Join(parts, ", "))

	// Failure details.
	for _, t := range tests {
		if t.Outcome != "failed" && t.Outcome != "crashed" {
			continue
		}
		outcome := strings.ToUpper(t.Outcome)
		fmt.Fprintf(w, "\n%s\n",
			bold(fmt.Sprintf("── %s ── %s %s", t.Test, outcome, strings.Repeat("─", 40))))

		report := &explain.Report{
			Test:            t.Test,
			Outcome:         t.Outcome,
			DurationMs:      t.DurationMs,
			Services:        t.Services,
			Assertions:      t.Assertions,
			Errors:          t.Errors,
			ServiceFailures: t.ServiceFailures,
			ServiceErrors:   t.ServiceErrors,
			Stall:           t.Stall,
		}
		if verbose {
			explain.Pretty(w, report)
		} else {
			s := explain.Condensed(report)
			if s != "" {
				fmt.Fprintln(w, s)
			}
		}
	}

	// Artifacts.
	if len(artifacts) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, bold("Artifacts:"))
		for _, a := range artifacts {
			if a.Cached {
				fmt.Fprintf(w, "  %-40s  %s\n", a.Key, dim("cached"))
			} else if a.DurationMs > 0 {
				fmt.Fprintf(w, "  %-40s  %s\n", a.Key, formatLsDuration(a.DurationMs))
			} else {
				fmt.Fprintf(w, "  %s\n", a.Key)
			}
		}
	}
}

func formatRunDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// --- Artifact event scanning ---

type artifactEvent struct {
	Type    string  `json:"type"`
	Key     string  `json:"key"`
	Elapsed float64 `json:"elapsed_ms"`
}

func scanArtifactEvents(path string) []ciArtifactJSON {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var results []ciArtifactJSON
	seen := map[string]bool{}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		var ev artifactEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "artifact.completed":
			if seen[ev.Key] {
				continue
			}
			seen[ev.Key] = true
			results = append(results, ciArtifactJSON{
				Key:        ev.Key,
				DurationMs: ev.Elapsed,
			})
		case "artifact.cached":
			if seen[ev.Key] {
				continue
			}
			seen[ev.Key] = true
			results = append(results, ciArtifactJSON{
				Key:    ev.Key,
				Cached: true,
			})
		}
	}
	return results
}

// --- Helpers ---

func ghJSON(args ...string) (json.RawMessage, error) {
	out, err := execOutput("gh", args...)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func execOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
