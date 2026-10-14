package github

import (
	"context"
	"net/url"
	"strconv"
)

const maxStepName = 200

// FailedStep names the step a failed workflow run of an allowlisted
// repository stopped at, for the release module's release.finished event.
// Step names are written by whoever can push to the repository: the release
// module cleans them and they are shown verbatim, never to the model.
func (s *Service) FailedStep(ctx context.Context, repo string, runID int64) (string, error) {
	r, cl, err := s.repoClient(ctx, repo)
	if err != nil {
		return "", err
	}
	var page struct {
		Jobs []ghJob `json:"jobs"`
	}
	q := url.Values{"filter": {"latest"}, "per_page": {"100"}}
	if _, err := cl.getJSON(ctx, repoPath(r)+"/actions/runs/"+strconv.FormatInt(runID, 10)+"/jobs", q, &page); err != nil {
		return "", err
	}
	for _, j := range page.Jobs {
		for _, st := range j.Steps {
			if st.Conclusion == "failure" || st.Conclusion == "timed_out" {
				return oneLine(st.Name, maxStepName), nil
			}
		}
	}
	return "", nil
}
