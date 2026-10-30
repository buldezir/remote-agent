package client

import "remote-agent/internal/model"

// The results and params on the wire that internal/model doesn't have,
// which rad keeps in internal/api, internal/fsbrowse and the like. ra
// mustn't link those, so they're described again here, and
// fixtures_test.go checks them against protocol/fixtures.

// ServerInfo is server.info's result, and the health check's fields.
type ServerInfo struct {
	ServerID        string   `json:"serverId"`
	Name            string   `json:"name"`
	ProtocolVersion int      `json:"protocolVersion"`
	Version         string   `json:"version"`
	Roots           []string `json:"roots,omitempty"`
	DeviceID        string   `json:"deviceId,omitempty"`
	Terminal        bool     `json:"terminal,omitempty"`
	// Drafts: the server keeps drafts (session.saveDraft, sendDraft and
	// session.create's draft).
	Drafts bool `json:"drafts,omitempty"`
}

// PairResult is what POST /v1/pair returns.
type PairResult struct {
	ServerID        string `json:"serverId"`
	Name            string `json:"name"`
	ProtocolVersion int    `json:"protocolVersion"`
	Version         string `json:"version"`
	Token           string `json:"token"`
	DeviceID        string `json:"deviceId"`
}

// FSListing is fs.list's result.
type FSListing struct {
	Path    string    `json:"path"`
	Parent  string    `json:"parent,omitempty"`
	Entries []FSEntry `json:"entries"`
}

type FSEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsGitRepo bool   `json:"isGitRepo"`
}

// Branches is git.branches' result.
type Branches struct {
	Branches []string `json:"branches"`
	Current  string   `json:"current"`
}

// NewSession is session.create's params, less the commandId.
type NewSession struct {
	ProjectID string           `json:"projectId"`
	Harness   string           `json:"harness"`
	Model     string           `json:"model,omitempty"`
	Effort    string           `json:"effort,omitempty"`
	Mode      string           `json:"mode,omitempty"`
	Workspace NewWorkspace     `json:"workspace"`
	Prompt    string           `json:"prompt,omitempty"`
	Images    []model.ImageRef `json:"-"`
	// Draft keeps the prompt and images as the session's draft rather than
	// sending them. Only when the server has drafts (ServerInfo.Drafts): an
	// older one would send them.
	Draft bool `json:"draft,omitempty"`
}

type NewWorkspace struct {
	Kind    model.WorkspaceKind `json:"kind"`
	Branch  string              `json:"branch,omitempty"`
	BaseRef string              `json:"baseRef,omitempty"`
}

// Notification params.

type eventsParams struct {
	Stream string        `json:"stream"`
	Events []model.Event `json:"events"`
}

type synchronizedParams struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
	Reset  bool   `json:"reset"`
}

type resyncParams struct {
	Stream string `json:"stream"`
}

// Results with a list in them.

type harnessList struct {
	Harnesses []model.HarnessInfo `json:"harnesses"`
}

type taskList struct {
	Tasks []model.Task `json:"tasks"`
}

type commandList struct {
	Commands []model.Command `json:"commands"`
}

type subscribeResult struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
}
