// Package marks reads and appends the "I didn't get this" moments of a class: a JSON array of seconds into the recording.
package marks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"

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

// Append adds sec, rounded to 0.1 s, to path, creating it if missing.
func Append(path string, sec float64) error {
	marks, err := Read(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b, err := json.Marshal(append(marks, math.Round(sec*10)/10))
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, b)
}
