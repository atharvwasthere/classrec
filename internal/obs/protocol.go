package obs

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// obs-websocket v5 opcodes.
const (
	opHello           = 0
	opIdentify        = 1
	opIdentified      = 2
	opEvent           = 5
	opRequest         = 6
	opRequestResponse = 7
)

const (
	rpcVersion = 1

	// Event subscriptions: recording state changes, plus the high-volume input level meters.
	subscribeOutputs           = 1 << 6
	subscribeInputVolumeMeters = 1 << 16

	closeAuthenticationFailed = 4009
)

// message is the envelope of every obs-websocket message.
type message struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
}

type hello struct {
	Authentication *struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication"`
}

type identify struct {
	RPCVersion         int    `json:"rpcVersion"`
	Authentication     string `json:"authentication,omitempty"`
	EventSubscriptions int    `json:"eventSubscriptions"`
}

type request struct {
	RequestType string `json:"requestType"`
	RequestID   string `json:"requestId"`
	RequestData any    `json:"requestData,omitempty"`
}

type requestResponse struct {
	RequestType   string `json:"requestType"`
	RequestID     string `json:"requestId"`
	RequestStatus struct {
		Result  bool   `json:"result"`
		Code    int    `json:"code"`
		Comment string `json:"comment"`
	} `json:"requestStatus"`
	ResponseData json.RawMessage `json:"responseData"`
}

type event struct {
	EventType string          `json:"eventType"`
	EventData json.RawMessage `json:"eventData"`
}

type inputVolumeMeters struct {
	Inputs []struct {
		InputName string `json:"inputName"`
		// One entry per channel: [magnitude, peak, input peak], each a linear multiplier.
		InputLevelsMul [][]float64 `json:"inputLevelsMul"`
	} `json:"inputs"`
}

// authResponse is base64(sha256(base64(sha256(password + salt)) + challenge)), as obs-websocket v5 specifies.
func authResponse(password, salt, challenge string) string {
	secretHash := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(secretHash[:])
	responseHash := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(responseHash[:])
}
