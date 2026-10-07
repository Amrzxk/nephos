// Package leakcheck performs non-destructive, fail-closed cleanup observation.
package leakcheck

import "github.com/Amrzxk/nephos/internal/model"

// Finding is the model DTO shared with durable reset results.
type Finding = model.LeakFinding

// Result is the complete/clean observation, not permission to delete objects.
type Result = model.LeakResult
