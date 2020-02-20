package common

type PollRequest struct {
	FQDN     string `json:"fqdn"`
	Launched int    `json:"launched"`
}

type PollReply struct {
	UUID    string `json:"uuid"`
	Request string `json:"request"`
}

type PushRequest struct {
	FQDN     string `json:"fqdn"`
	UUID     string `json:"uuid"`
	Response string `json:"response"`
}
