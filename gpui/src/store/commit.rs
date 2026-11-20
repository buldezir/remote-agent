//! Committing a session's work: how far a commit goes, as the form offers
//! it (ra's `internal/client/commit.go`, RAKit's CommitView).

use crate::protocol::CommitPreview;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CommitScope {
    Commit,
    Push,
    PullRequest,
}

pub const COMMIT_SCOPES: [CommitScope; 3] = [CommitScope::Commit, CommitScope::Push, CommitScope::PullRequest];

impl CommitScope {
    /// The scope as the form offers it.
    pub fn choice(self) -> &'static str {
        match self {
            CommitScope::Commit => "Commit only",
            CommitScope::Push => "Commit & push",
            CommitScope::PullRequest => "Commit, push & open PR",
        }
    }

    /// The button that starts it: with nothing to commit, it only pushes.
    pub fn action(self, files: bool) -> &'static str {
        match (self, files) {
            (CommitScope::Push, true) => "Commit & Push",
            (CommitScope::Push, false) => "Push",
            (CommitScope::PullRequest, true) => "Commit, Push & Open PR",
            (CommitScope::PullRequest, false) => "Push & Open PR",
            (CommitScope::Commit, _) => "Commit",
        }
    }

    /// The scope as settings keep it.
    pub fn key(self) -> &'static str {
        match self {
            CommitScope::Commit => "commit",
            CommitScope::Push => "push",
            CommitScope::PullRequest => "pullRequest",
        }
    }

    /// Why the scope can't go ahead with what the preview found, if it can't.
    pub fn unavailable(self, p: &CommitPreview) -> Option<String> {
        if self == CommitScope::Commit {
            return p.files.is_empty().then(|| "Nothing to commit.".into());
        }
        if p.branch.as_deref().unwrap_or("").is_empty() {
            return Some("HEAD is detached: there's no branch to push.".into());
        }
        if p.remote.as_deref().unwrap_or("").is_empty() {
            return Some("No remote to push to.".into());
        }
        if self == CommitScope::PullRequest
            && let Some(why) = p.no_pull_request.as_deref().filter(|w| !w.is_empty())
        {
            return Some(if why.ends_with('.') {
                why.to_string()
            } else {
                format!("{why}.")
            });
        }
        None
    }

    /// The scope a form starts at: the one saved where it can go ahead; else
    /// commit and push with a remote, else commit only.
    pub fn default_for(saved: &str, p: &CommitPreview) -> CommitScope {
        if let Some(s) = COMMIT_SCOPES
            .iter()
            .find(|s| s.key() == saved && s.unavailable(p).is_none())
        {
            return *s;
        }
        if CommitScope::Push.unavailable(p).is_none() {
            CommitScope::Push
        } else {
            CommitScope::Commit
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scopes() {
        let mut p = CommitPreview {
            branch: Some("ra/x".into()),
            remote: Some("origin".into()),
            files: vec![],
            ..Default::default()
        };
        assert_eq!(
            CommitScope::Commit.unavailable(&p).as_deref(),
            Some("Nothing to commit.")
        );
        assert_eq!(CommitScope::Push.unavailable(&p), None);
        assert_eq!(CommitScope::Push.action(false), "Push");
        assert_eq!(CommitScope::default_for("pullRequest", &p), CommitScope::PullRequest);
        p.no_pull_request = Some("main is the repository's default branch".into());
        assert_eq!(
            CommitScope::PullRequest.unavailable(&p).as_deref(),
            Some("main is the repository's default branch.")
        );
        assert_eq!(CommitScope::default_for("pullRequest", &p), CommitScope::Push);
        p.remote = None;
        assert_eq!(CommitScope::default_for("push", &p), CommitScope::Commit);
        p.branch = None;
        assert!(CommitScope::Push.unavailable(&p).unwrap().contains("detached"));
    }
}
