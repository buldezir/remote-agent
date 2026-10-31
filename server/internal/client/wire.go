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
	// Forks: the server forks sessions (session.fork).
	Forks bool `json:"forks,omitempty"`
	// Commit: the server commits, pushes and opens pull requests
	// (git.commitPreview, git.commit).
	Commit bool `json:"commit,omitempty"`
	// Search: the server finds sessions by what was said in them
	// (session.search).
	Search bool `json:"search,omitempty"`
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

// ForkSession is session.fork's params, less the commandId: a session that
// goes on from SessionID after turn Turn. What's left empty is the
// source's, and with another Harness, that harness's defaults. Only when
// the server forks (ServerInfo.Forks).
type ForkSession struct {
	SessionID string
	Turn      int // 0: the last turn that ended
	Harness   string
	Model     string
	Effort    string
	Mode      string
	// Workspace: nil keeps the source's kind. A worktree starts with the
	// files as they were after the turn, so BaseRef is left out.
	Workspace *NewWorkspace
	Prompt    string
	Images    []model.ImageRef
	Title     string
	Draft     bool
}

// ImportSession is session.import's params, less the commandId: the
// agent's own session NativeID, which ran in the project's folder outside
// rad (Importable lists them). Model, Effort and Mode are what it goes on
// with; empty, the harness's defaults. Only for harnesses with caps.import.
type ImportSession struct {
	ProjectID string `json:"projectId"`
	Harness   string `json:"harness"`
	NativeID  string `json:"nativeId"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// CommitPreview is git.commitPreview's result: what git.commit would
// commit in a session, and a message for it.
type CommitPreview struct {
	Branch  string     `json:"branch,omitempty"` // absent: detached
	Files   []FileStat `json:"files"`
	Message string     `json:"message"` // "" with no files
	Remote  string     `json:"remote,omitempty"`
	Base    string     `json:"base,omitempty"`
	Ahead   int        `json:"ahead"`
}

// FileStat is a file a diff or a commit changes.
type FileStat struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status"` // added, modified, deleted, renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// CommitParams are git.commit's: what to go as far as.
type CommitParams struct {
	SessionID   string `json:"sessionId"`
	Message     string `json:"message,omitempty"`
	Push        bool   `json:"push,omitempty"`
	PullRequest bool   `json:"pullRequest,omitempty"`
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

type importableList struct {
	Sessions []model.ImportableSession `json:"sessions"`
}

type commandList struct {
	Commands []model.Command `json:"commands"`
}

type subscribeResult struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
}
