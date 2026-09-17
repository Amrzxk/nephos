package main

// Verdict reporting, in the same PASS/FAIL/metric shape the shell spikes use,
// so all four spike reports read alike.

import (
	"fmt"
	"os"
	"strings"
)

// Report accumulates assertions and metrics.
type Report struct {
	Passed  int
	Failed  int
	entries []entry
}

type entry struct {
	kind     string // PASS, FAIL, METRIC
	desc     string
	evidence string
}

// NewReport returns an empty report.
func NewReport() *Report { return &Report{} }

// Section prints a heading.
func (r *Report) Section(name string) {
	fmt.Printf("\n\033[1m=== %s ===\033[0m\n", name)
}

// Assert records one verdict. A false condition is a result, not a crash: the
// spike keeps going so that one failure does not hide the others.
func (r *Report) Assert(desc string, ok bool, evidence string) {
	if ok {
		fmt.Printf("  \033[32mPASS\033[0m    %s\n", desc)
		if evidence != "" {
			fmt.Printf("          %s\n", evidence)
		}
		r.Passed++
		r.entries = append(r.entries, entry{"PASS", desc, evidence})
		return
	}
	fmt.Printf("  \033[31mFAIL\033[0m    %s\n", desc)
	if evidence != "" {
		fmt.Printf("          %s\n", evidence)
	}
	r.Failed++
	r.entries = append(r.entries, entry{"FAIL", desc, evidence})
}

// Fail records an outright error in a step.
func (r *Report) Fail(desc, evidence string) {
	r.Assert(desc, false, evidence)
}

// Metric records a measurement, which carries no verdict of its own.
func (r *Report) Metric(name, value string) {
	fmt.Printf("  \033[36mmetric\033[0m  %-44s %s\n", name, value)
	r.entries = append(r.entries, entry{"METRIC", name, value})
}

// Summary prints the totals.
func (r *Report) Summary() {
	fmt.Printf("\n  \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n", r.Passed, r.Failed)
}

// WriteTSV writes the verdicts where run.sh can copy them out of the appliance.
func (r *Report) WriteTSV(path string) error {
	var b strings.Builder
	for _, e := range r.entries {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.kind, e.desc, e.evidence)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
