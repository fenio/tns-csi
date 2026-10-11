package tnsapi

import (
	"errors"
	"strings"
)

// Error classification for TrueNAS API errors.
//
// This is the one place that decides what kind of failure an error is. Each classifier
// checks the structured TrueNAS errno name (Error.ErrorName or Error.Data.ErrorName,
// found anywhere in a wrapped chain) and then falls back to known message text, because
// some errors reach callers only as text (older middleware, job results, mocks).
// Callers must use these instead of matching err.Error() themselves.

// errNames returns the TrueNAS errno names carried by the first *Error in err's chain.
func errNames(err error) []string {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return nil
	}
	var names []string
	if apiErr.ErrorName != "" {
		names = append(names, apiErr.ErrorName)
	}
	if apiErr.Data != nil && apiErr.Data.ErrorName != "" {
		names = append(names, apiErr.Data.ErrorName)
	}
	return names
}

// classify reports whether err carries one of the errno names or one of the message
// fragments (matched case-insensitively).
func classify(err error, names, fragments []string) bool {
	if err == nil {
		return false
	}
	for _, n := range errNames(err) {
		for _, want := range names {
			if n == want {
				return true
			}
		}
	}
	msg := strings.ToLower(err.Error())
	for _, f := range fragments {
		if strings.Contains(msg, strings.ToLower(f)) {
			return true
		}
	}
	return false
}

// IsNotFound reports whether err means the target resource does not exist.
// Delete paths treat this as "already deleted", so it must not match anything else.
func IsNotFound(err error) bool {
	if errors.Is(err, ErrDatasetNotFound) {
		return true
	}
	return classify(err, []string{"ENOENT"}, []string{"not found", "does not exist", "ENOENT"})
}

// IsAlreadyExists reports whether err means the resource being created already exists.
func IsAlreadyExists(err error) bool {
	return classify(err, []string{"EEXIST"}, []string{"already exists"})
}

// IsBusy reports whether err means the resource is temporarily in use (retry later).
func IsBusy(err error) bool {
	return classify(err, []string{"EBUSY"}, []string{
		"dataset is busy", "target is busy", "resource busy", "ebusy", "ezfs_busy",
		"device or resource busy", "pool is busy", "filesystem is busy",
	})
}

// IsDependentClones reports whether err means a dataset or ZVOL cannot be destroyed
// because ZFS clones depend on it. This never resolves until the clones are deleted.
func IsDependentClones(err error) bool {
	return classify(err, nil, []string{"dependent clones"})
}

// IsCapacity reports whether err means the pool or a quota is out of space.
func IsCapacity(err error) bool {
	return classify(err, []string{"ENOSPC", "EDQUOT"}, []string{
		"insufficient space", "out of space", "not enough space", "no space left", "ENOSPC", "quota exceeded",
	})
}
