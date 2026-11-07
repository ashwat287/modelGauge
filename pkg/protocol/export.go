package protocol

// EvidenceRef identifies a raw file retained on a measured node. It is not a URL.
type EvidenceRef struct {
	RunID  string `json:"run_id"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}
