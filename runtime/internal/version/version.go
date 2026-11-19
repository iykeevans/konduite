// Package version carries build metadata stamped in with -ldflags -X.
package version

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Info describes the running build.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Get returns the stamped build info, or the dev placeholders.
func Get() Info {
	return Info{Version: version, Commit: commit, Date: date}
}

func (i Info) String() string {
	return i.Version + " (commit " + i.Commit + ", built " + i.Date + ")"
}
