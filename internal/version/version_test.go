package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestShortCommit(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "full sha is abbreviated", in: "c20f1aa9b8e7d6c5b4a39281706f5e4d3c2b1a09", want: "c20f1aa"},
		{name: "exactly seven is untouched", in: "c20f1aa", want: "c20f1aa"},
		{name: "shorter than seven is untouched", in: "c20f", want: "c20f"},
		{name: "placeholder is untouched", in: "unknown", want: "unknown"},
		{name: "empty stays empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortCommit(tt.in); got != tt.want {
				t.Errorf("shortCommit(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInfoString(t *testing.T) {
	tests := []struct {
		name        string
		info        Info
		wantContain []string
		wantOmit    []string
	}{
		{
			name:        "release build shows abbreviated commit",
			info:        Info{Version: "v0.1.0", Commit: "c20f1aa9b8e7d6c5", Date: "2026-09-17", GoVersion: "go1.27.1", Platform: "linux/amd64"},
			wantContain: []string{"v0.1.0", "(c20f1aa)", "go1.27.1", "linux/amd64"},
		},
		{
			name:        "unknown commit is not rendered",
			info:        Info{Version: "dev", Commit: "unknown", GoVersion: "go1.27.1", Platform: "linux/amd64"},
			wantContain: []string{"dev", "go1.27.1"},
			wantOmit:    []string{"("},
		},
		{
			name:        "empty commit is not rendered",
			info:        Info{Version: "dev", Commit: "", GoVersion: "go1.27.1", Platform: "linux/arm64"},
			wantContain: []string{"dev", "linux/arm64"},
			wantOmit:    []string{"("},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.String()
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("String() = %q, want it to contain %q", got, want)
				}
			}
			for _, omit := range tt.wantOmit {
				if strings.Contains(got, omit) {
					t.Errorf("String() = %q, want it to omit %q", got, omit)
				}
			}
		})
	}
}

func TestInfoIsRelease(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want bool
	}{
		{name: "stamped build", info: Info{Version: "v0.1.0", Commit: "c20f1aa"}, want: true},
		{name: "default dev build", info: Info{Version: "dev", Commit: "unknown"}, want: false},
		{name: "version set but commit missing", info: Info{Version: "v0.1.0", Commit: "unknown"}, want: false},
		{name: "commit set but version still dev", info: Info{Version: "dev", Commit: "c20f1aa"}, want: false},
		{name: "empty version", info: Info{Version: "", Commit: "c20f1aa"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.IsRelease(); got != tt.want {
				t.Errorf("IsRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGetReportsRuntime pins the two fields Get fills in itself, so a refactor
// that drops them is caught even though the ldflags values are build-dependent.
func TestGetReportsRuntime(t *testing.T) {
	got := Get()
	if got.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
	wantPlatform := runtime.GOOS + "/" + runtime.GOARCH
	if got.Platform != wantPlatform {
		t.Errorf("Platform = %q, want %q", got.Platform, wantPlatform)
	}
	if got.Version == "" {
		t.Error("Version is empty; the ldflags default should always be set")
	}
}
