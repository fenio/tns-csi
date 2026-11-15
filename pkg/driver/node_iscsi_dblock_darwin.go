package driver

import (
	"os"
	"syscall"
	"time"
)

// fileChangeTime returns the inode change time (ctime) of info.
func fileChangeTime(info os.FileInfo) (time.Time, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(stat.Ctimespec.Sec, stat.Ctimespec.Nsec), true
}
