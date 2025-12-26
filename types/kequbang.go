package types

// Kequbang WS Protocol Messages

type WSMessage struct {
	Type        string `json:"type"`
	Content     string `json:"content,omitempty"`
	DialogID    string `json:"dialogId,omitempty"`
	UserID      string `json:"userId,omitempty"`
	SendType    string `json:"sendType,omitempty"`    // "0": Audio, "1": Text
	ReceiveType string `json:"receiveType,omitempty"` // "0": Audio only, "1": Text only, "2": Both
	Text        string `json:"text,omitempty"`
}

const (
	MsgTypeHeartbeat      = "HEARTBEAT"
	MsgTypeStart          = "start"
	MsgTypeStartSpeech    = "startSpeech"
	MsgTypeSendSpeechText = "sendSpeechText"
	MsgTypeStopSpeech     = "stopSpeech"
	MsgTypeAudio          = "AUDIO"
	MsgTypeText           = "text"
	MsgTypeNoSpeech       = "noSpeech"
	MsgTypePlayOver       = "playOver"
)
