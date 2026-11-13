package model

// LeakFinding is engine-independent evidence of an owned object/reference.
type LeakFinding struct {
	Kind     string `json:"kind"`
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
}

// LeakResult distinguishes incomplete observation from observed residue.
type LeakResult struct {
	Complete         bool          `json:"complete"`
	Clean            bool          `json:"clean"`
	Findings         []LeakFinding `json:"findings"`
	InspectionErrors []string      `json:"inspection_errors"`
}
