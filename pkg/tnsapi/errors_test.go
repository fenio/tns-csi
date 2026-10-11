package tnsapi

import (
	"errors"
	"fmt"
	"testing"
)

// apiErr builds the error shape TrueNAS returns over JSON-RPC.
func apiErr(errname, reason string) error {
	return &Error{ErrorName: errname, Reason: reason, ErrorCode: 22}
}

// dataErr builds the JSON-RPC 2.0 variant where errname lives in Data.
func dataErr(errname, reason string) error {
	return &Error{Code: -32001, Message: "Method call error", Data: &ErrorData{ErrorName: errname, Reason: reason}}
}

func TestClassifiers(t *testing.T) {
	type want struct{ notFound, exists, busy, clones, capacity bool }
	tests := []struct {
		err  error
		name string
		want want
	}{
		{name: "nil", err: nil},
		{name: "unrelated", err: errors.New("validation failed: name too long")},

		// Not found: structured errname must count even when the reason text does not
		// say "not found" (callers treat not-found as "already deleted").
		{name: "ENOENT errname with terse reason", err: apiErr("ENOENT", "tank/csi/pvc-1"), want: want{notFound: true}},
		{name: "ENOENT in Data", err: dataErr("ENOENT", "missing"), want: want{notFound: true}},
		{name: "ENOENT wrapped", err: fmt.Errorf("delete dataset: %w", apiErr("ENOENT", "x")), want: want{notFound: true}},
		{name: "sentinel ErrDatasetNotFound", err: fmt.Errorf("lookup: %w", ErrDatasetNotFound), want: want{notFound: true}},
		{name: "legacy text: does not exist", err: errors.New("[EINVAL] dataset tank/x does not exist"), want: want{notFound: true}},
		{name: "legacy text: not found", err: errors.New("dataset not found"), want: want{notFound: true}},

		{name: "EEXIST", err: apiErr("EEXIST", "tank/csi/pvc-1"), want: want{exists: true}},
		{name: "legacy text: already exists", err: errors.New("share already exists"), want: want{exists: true}},

		{name: "EBUSY", err: apiErr("EBUSY", "target busy"), want: want{busy: true}},
		{name: "legacy text: dataset is busy", err: errors.New("cannot destroy: dataset is busy"), want: want{busy: true}},

		{
			name: "dependent clones",
			err:  apiErr("EFAULT", "cannot destroy 'tank/v': volume has dependent clones"),
			want: want{clones: true},
		},

		{name: "ENOSPC", err: apiErr("ENOSPC", "pool full"), want: want{capacity: true}},
		{name: "EDQUOT", err: apiErr("EDQUOT", "quota"), want: want{capacity: true}},
		{name: "legacy text: out of space", err: errors.New("insufficient space on pool"), want: want{capacity: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := want{
				notFound: IsNotFound(tt.err),
				exists:   IsAlreadyExists(tt.err),
				busy:     IsBusy(tt.err),
				clones:   IsDependentClones(tt.err),
				capacity: IsCapacity(tt.err),
			}
			if got != tt.want {
				t.Errorf("classification = %+v, want %+v (err: %v)", got, tt.want, tt.err)
			}
		})
	}
}

func FuzzClassifiers(f *testing.F) {
	f.Add("ENOENT", "missing")
	f.Add("", "dependent clones")
	f.Add("EBUSY", "")
	f.Fuzz(func(_ *testing.T, errname, reason string) {
		for _, err := range []error{apiErr(errname, reason), dataErr(errname, reason), errors.New(reason)} {
			_ = IsNotFound(err)
			_ = IsAlreadyExists(err)
			_ = IsBusy(err)
			_ = IsDependentClones(err)
			_ = IsCapacity(err)
		}
	})
}
