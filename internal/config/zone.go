package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NameLocalZone gives time.Local its IANA name. Without TZ set, Go calls the
// system zone "Local", which browsers reject as a time zone; the name is read
// from /etc/timezone or the /etc/localtime link instead, and UTC is used when
// neither says.
func NameLocalZone() {
	if time.Local.String() != "Local" {
		return
	}
	name := ""
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		name = strings.TrimSpace(string(b))
	}
	if name == "" {
		if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
			if i := strings.Index(p, "zoneinfo/"); i >= 0 {
				name = p[i+len("zoneinfo/"):]
			}
		}
	}
	if loc, err := time.LoadLocation(name); err == nil && name != "" && name != "Local" {
		time.Local = loc
		return
	}
	time.Local = time.UTC
}
