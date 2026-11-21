//! A session's pull request in words, as every client words it: its state,
//! checks and review, the status line that joins them, and the label a
//! screen reader reads.

use crate::protocol::{PullRequest, PullRequestChecks, PullRequestReview, PullRequestState};

impl PullRequest {
    /// Its state, with one this client doesn't know shown as open.
    pub fn shown_state(&self) -> PullRequestState {
        match self.state {
            PullRequestState::Unknown => PullRequestState::Open,
            s => s,
        }
    }

    /// Its checks, unless this client doesn't know what they say.
    pub fn shown_checks(&self) -> Option<PullRequestChecks> {
        self.checks.filter(|c| *c != PullRequestChecks::Unknown)
    }

    /// Its review, unless this client doesn't know what it says.
    pub fn shown_review(&self) -> Option<PullRequestReview> {
        self.review.filter(|r| *r != PullRequestReview::Unknown)
    }

    /// Open, Draft, Merged or Closed.
    pub fn state_label(&self) -> &'static str {
        match self.shown_state() {
            PullRequestState::Draft => "Draft",
            PullRequestState::Merged => "Merged",
            PullRequestState::Closed => "Closed",
            PullRequestState::Open | PullRequestState::Unknown => "Open",
        }
    }

    pub fn checks_label(&self) -> Option<&'static str> {
        Some(match self.shown_checks()? {
            PullRequestChecks::Passing => "Checks passing",
            PullRequestChecks::Failing => "Checks failing",
            PullRequestChecks::Pending | PullRequestChecks::Unknown => "Checks running",
        })
    }

    pub fn review_label(&self) -> Option<&'static str> {
        Some(match self.shown_review()? {
            PullRequestReview::Approved => "Approved",
            PullRequestReview::ChangesRequested => "Changes requested",
            PullRequestReview::ReviewRequired | PullRequestReview::Unknown => "Review required",
        })
    }

    fn parts(&self) -> Vec<&'static str> {
        [Some(self.state_label()), self.checks_label(), self.review_label()]
            .into_iter()
            .flatten()
            .collect()
    }

    /// "Open · Checks failing · Changes requested", leaving out what's absent.
    pub fn status_line(&self) -> String {
        self.parts().join(" · ")
    }

    /// "#42 Fix the flaky tests".
    pub fn headline(&self) -> String {
        if self.title.is_empty() {
            format!("#{}", self.number)
        } else {
            format!("#{} {}", self.number, self.title)
        }
    }

    /// "Pull request #42, Open, Checks failing, Changes requested".
    pub fn accessibility_label(&self) -> String {
        let mut parts = vec![format!("Pull request #{}", self.number)];
        parts.extend(self.parts().into_iter().map(String::from));
        parts.join(", ")
    }

    /// The forge by name: GitHub, else the host in its link.
    pub fn forge_name(&self) -> String {
        match self.forge.as_str() {
            "github" => "GitHub".into(),
            other => url::Url::parse(&self.url)
                .ok()
                .and_then(|u| u.host_str().map(String::from))
                .unwrap_or_else(|| other.to_string()),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pr(state: &str, checks: Option<&str>, review: Option<&str>) -> PullRequest {
        serde_json::from_value(serde_json::json!({
            "forge": "github", "number": 42, "title": "Fix the flaky tests",
            "url": "https://github.com/acme/app/pull/42", "state": state, "head": "ra/fix-tests",
            "checks": checks, "review": review,
        }))
        .unwrap()
    }

    #[test]
    fn status_line_leaves_out_what_is_absent() {
        let failing = pr("open", Some("failing"), Some("changes_requested"));
        assert_eq!(failing.status_line(), "Open · Checks failing · Changes requested");
        assert_eq!(
            pr("merged", Some("passing"), Some("approved")).status_line(),
            "Merged · Checks passing · Approved"
        );
        assert_eq!(pr("closed", None, None).status_line(), "Closed");
        assert_eq!(
            pr("draft", Some("pending"), Some("review_required")).status_line(),
            "Draft · Checks running · Review required"
        );
    }

    #[test]
    fn unknown_values_show_as_open_without_checks_or_review() {
        let p = pr("queued", Some("skipped"), Some("dismissed"));
        assert_eq!(p.shown_state(), PullRequestState::Open);
        assert_eq!(p.status_line(), "Open");
        assert_eq!(p.shown_checks(), None);
    }

    #[test]
    fn labels() {
        let p = pr("open", Some("failing"), Some("changes_requested"));
        assert_eq!(
            p.accessibility_label(),
            "Pull request #42, Open, Checks failing, Changes requested"
        );
        assert_eq!(p.headline(), "#42 Fix the flaky tests");
        assert_eq!(p.forge_name(), "GitHub");
        let mut other = p.clone();
        other.forge = "gitlab".into();
        other.url = "https://gitlab.example.com/a/b/-/merge_requests/42".into();
        assert_eq!(other.forge_name(), "gitlab.example.com");
    }
}
