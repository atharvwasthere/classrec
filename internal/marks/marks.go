// Package marks reads and appends the "I didn't get this" moments of a class. During a class they
// are wall-clock times (AppendTime); `classrec process --marks` takes seconds into one video (Read).
package marks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"classrec/internal/fsx"
)

// Read returns the marks in path.
func Read(path string) ([]float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var marks []float64
	if err := json.Unmarshal(b, &marks); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of seconds: %w", path, err)
	}
	for _, m := range marks {
		if m < 0 {
			return nil, fmt.Errorf("%s: mark %v is negative", path, m)
		}
	}
	return marks, nil
}

// ReadTimes returns the wall-clock marks in path, the form the overlay writes during a class. A
// mark is the moment the button was pressed, not an offset, because the recording may be several
// pieces; the pipeline finds each mark in the joined recording using the class's pieces.json.
func ReadTimes(path string) ([]time.Time, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var times []time.Time
	if err := json.Unmarshal(b, &times); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of RFC 3339 times: %w", path, err)
	}
	return times, nil
}

// AppendTime adds the moment t to path, creating it if missing. Like Append it leaves a file it
// cannot read untouched.
func AppendTime(path string, t time.Time) error {
	times, err := ReadTimes(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b, err := json.Marshal(append(times, t.Round(time.Millisecond)))
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, b)
}
