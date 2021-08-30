// Copyright 2021 Security Scorecard Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package githubrepo

import (
	"context"
	"fmt"
	"sync"

	"github.com/shurcooL/githubv4"

	"github.com/ossf/scorecard/v2/clients"
	sce "github.com/ossf/scorecard/v2/errors"
)

const (
	pullRequestsToAnalyze  = 30
	reviewsToAnalyze       = 30
	labelsToAnalyze        = 30
	commitsToAnalyze       = 30
	releasesToAnalyze      = 30
	releaseAssetsToAnalyze = 30
)

// nolint: govet
type graphqlData struct {
	Repository struct {
		IsArchived       githubv4.Boolean
		DefaultBranchRef struct {
			Target struct {
				Commit struct {
					History struct {
						Nodes []struct {
							CommittedDate githubv4.DateTime
							Message       githubv4.String
							Oid           githubv4.GitObjectID
							Committer     struct {
								User struct {
									Login githubv4.String
								}
							}
						}
					} `graphql:"history(first: $commitsToAnalyze)"`
				} `graphql:"... on Commit"`
			}
		}
		PullRequests struct {
			Nodes []struct {
				Number      githubv4.Int
				HeadRefOid  githubv4.String
				MergeCommit struct {
					AuthoredByCommitter githubv4.Boolean
				}
				MergedAt githubv4.DateTime
				Labels   struct {
					Nodes []struct {
						Name githubv4.String
					}
				} `graphql:"labels(last: $labelsToAnalyze)"`
				LatestReviews struct {
					Nodes []struct {
						State githubv4.String
					}
				} `graphql:"latestReviews(last: $reviewsToAnalyze)"`
			}
		} `graphql:"pullRequests(last: $pullRequestsToAnalyze, states: MERGED)"`
		Releases struct {
			Nodes []struct {
				TagName       githubv4.String
				ReleaseAssets struct {
					Nodes []struct {
						Name githubv4.String
						URL  githubv4.String
					}
				} `graphql:"releaseAssets(last: $releaseAssetsToAnalyze)"`
			}
		} `graphql:"releases(first: $releasesToAnalyze, orderBy:{field: CREATED_AT, direction:DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type graphqlHandler struct {
	client   *githubv4.Client
	data     *graphqlData
	once     *sync.Once
	ctx      context.Context
	errSetup error
	owner    string
	repo     string
	prs      []clients.PullRequest
	commits  []clients.Commit
	releases []clients.Release
	archived bool
}

func (handler *graphqlHandler) init(ctx context.Context, owner, repo string) {
	handler.ctx = ctx
	handler.owner = owner
	handler.repo = repo
	handler.data = new(graphqlData)
	handler.errSetup = nil
	handler.once = new(sync.Once)
}

func (handler *graphqlHandler) setup() error {
	handler.once.Do(func() {
		vars := map[string]interface{}{
			"owner":                  githubv4.String(handler.owner),
			"name":                   githubv4.String(handler.repo),
			"pullRequestsToAnalyze":  githubv4.Int(pullRequestsToAnalyze),
			"reviewsToAnalyze":       githubv4.Int(reviewsToAnalyze),
			"labelsToAnalyze":        githubv4.Int(labelsToAnalyze),
			"commitsToAnalyze":       githubv4.Int(commitsToAnalyze),
			"releasesToAnalyze":      githubv4.Int(releasesToAnalyze),
			"releaseAssetsToAnalyze": githubv4.Int(releaseAssetsToAnalyze),
		}
		if err := handler.client.Query(handler.ctx, handler.data, vars); err != nil {
			handler.errSetup = sce.Create(sce.ErrScorecardInternal, fmt.Sprintf("githubv4.Query: %v", err))
		}
		handler.archived = bool(handler.data.Repository.IsArchived)
		handler.prs = pullRequestsFrom(handler.data)
		handler.releases = releasesFrom(handler.data)
		handler.commits = commitsFrom(handler.data)
	})
	return handler.errSetup
}

func (handler *graphqlHandler) getMergedPRs() ([]clients.PullRequest, error) {
	if err := handler.setup(); err != nil {
		return nil, fmt.Errorf("error during graphqlHandler.setup: %w", err)
	}
	return handler.prs, nil
}

func (handler *graphqlHandler) getCommits() ([]clients.Commit, error) {
	if err := handler.setup(); err != nil {
		return nil, fmt.Errorf("error during graphqlHandler.setup: %w", err)
	}
	return handler.commits, nil
}

func (handler *graphqlHandler) getReleases() ([]clients.Release, error) {
	if err := handler.setup(); err != nil {
		return nil, fmt.Errorf("error during graphqlHandler.setup: %w", err)
	}
	return handler.releases, nil
}

func (handler *graphqlHandler) isArchived() (bool, error) {
	if err := handler.setup(); err != nil {
		return false, fmt.Errorf("error during graphqlHandler.setup: %w", err)
	}
	return handler.archived, nil
}

func pullRequestsFrom(data *graphqlData) []clients.PullRequest {
	ret := make([]clients.PullRequest, len(data.Repository.PullRequests.Nodes))
	for i, pr := range data.Repository.PullRequests.Nodes {
		toAppend := clients.PullRequest{
			Number:   int(pr.Number),
			HeadSHA:  string(pr.HeadRefOid),
			MergedAt: pr.MergedAt.Time,
			MergeCommit: clients.Commit{
				AuthoredByCommitter: bool(pr.MergeCommit.AuthoredByCommitter),
			},
		}
		for _, label := range pr.Labels.Nodes {
			toAppend.Labels = append(toAppend.Labels, clients.Label{
				Name: string(label.Name),
			})
		}
		for _, review := range pr.LatestReviews.Nodes {
			toAppend.Reviews = append(toAppend.Reviews, clients.Review{
				State: string(review.State),
			})
		}
		ret[i] = toAppend
	}
	return ret
}

func releasesFrom(data *graphqlData) []clients.Release {
	// nolint: prealloc // https://github.com/golang/go/wiki/CodeReviewComments#declaring-empty-slices
	var releases []clients.Release
	for _, r := range data.Repository.Releases.Nodes {
		release := clients.Release{
			TagName: string(r.TagName),
		}
		for _, a := range r.ReleaseAssets.Nodes {
			release.Assets = append(release.Assets, clients.ReleaseAsset{
				Name: string(a.Name),
				URL:  string(a.URL),
			})
		}
		releases = append(releases, release)
	}
	return releases
}

func commitsFrom(data *graphqlData) []clients.Commit {
	ret := make([]clients.Commit, 0)
	for _, commit := range data.Repository.DefaultBranchRef.Target.Commit.History.Nodes {
		ret = append(ret, clients.Commit{
			CommittedDate: commit.CommittedDate.Time,
			Message:       string(commit.Message),
			SHA:           string(commit.Oid),
			Committer: clients.User{
				Login: string(commit.Committer.User.Login),
			},
		})
	}
	return ret
}
