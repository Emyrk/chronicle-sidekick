package pair

// StartRequest begins pairing a Chronicle Sidekick installation.
type StartRequest struct {
	DeviceName string `json:"device_name"`
}

// StartResponse describes the browser approval flow returned by Chronicle.
type StartResponse struct {
	Code       string `json:"code"`
	DeviceCode string `json:"device_code"`
	ExpiresIn  int    `json:"expires_in"`
	VerifyURL  string `json:"verify_url"`
	Interval   int    `json:"interval"`
}

// PollRequest checks whether a pairing request has been approved.
type PollRequest struct {
	DeviceCode string `json:"device_code"`
}

// PairStatus is the server-reported state of a pairing request.
type PairStatus string

const (
	PairStatusPending  PairStatus = "pending"
	PairStatusDenied   PairStatus = "denied"
	PairStatusExpired  PairStatus = "expired"
	PairStatusApproved PairStatus = "approved"
)

// PollResponse is returned while Sidekick waits for browser approval.
type PollResponse struct {
	Status PairStatus `json:"status"`
	Token  string     `json:"token,omitempty"`
}

// WhoAmIResponse identifies the device associated with a device token.
type WhoAmIResponse struct {
	DeviceName string `json:"device_name"`
}
