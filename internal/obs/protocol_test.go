package obs

import (
	"math"
	"testing"
)

// The worked example from the obs-websocket v5 protocol documentation.
func TestAuthResponseMatchesProtocolDocs(t *testing.T) {
	got := authResponse("supersecretpassword",
		"lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI=",
		"+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY=")
	want := "1Ct943GAT+6YQUUX47Ia/ncufilbe6+oD6lY+5kaCu4="
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestLoudestChannelDB(t *testing.T) {
	tests := []struct {
		channels [][]float64
		want     float64
	}{
		{[][]float64{{1, 1, 1}}, 0},
		{[][]float64{{0.01, 0.5, 0.5}, {0.1, 0.2, 0.2}}, -20}, // magnitude, not peak; loudest channel
		{[][]float64{{0, 0, 0}}, math.Inf(-1)},
		{nil, math.Inf(-1)},
	}
	for _, tt := range tests {
		if got := loudestChannelDB(tt.channels); math.Abs(got-tt.want) > 1e-9 && got != tt.want {
			t.Errorf("%v: got %v, want %v", tt.channels, got, tt.want)
		}
	}
}
