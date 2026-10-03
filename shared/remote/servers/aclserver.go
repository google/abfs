// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package servers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

type ACLServer struct {
	remote.RemoteCloser
	evaluator *remote.ACLEvaluator
}

var _ remote.RemoteCloser = (*ACLServer)(nil)

func NewACLServer(server remote.RemoteCloser, evaluator *remote.ACLEvaluator) remote.RemoteCloser {
	return &ACLServer{
		RemoteCloser: server,
		evaluator:    evaluator,
	}
}

// isMatchingUserRef checks if a ref matches the pattern "refs/abfs/*/user/<user>/*" for a given user.
// **Important**:
// Refs are stored by the username on the users host, but the email we use for ACL
// purposes is the gcloud email address. For users on managed workstations, we are assuming these are the same.
func (s *ACLServer) isMatchingUserRef(server, project, ref, email string) bool {
	parts := strings.Split(ref, "/")
	user := strings.Split(email, "@")[0]

	// Match against `refs/abfs/*/user/<user>/*`
	if len(parts) >= 5 && parts[0] == "refs" && parts[1] == "abfs" && parts[3] == "user" && parts[4] == user {
		return true
	}

	// Match against `refs/abfs/ws/user/<user>/*`
	if len(parts) >= 5 && parts[0] == "refs" && parts[1] == "abfs" && parts[2] == "ws" && parts[3] == "user" && parts[4] == user {
		return true
	}

	return false
}

func (s *ACLServer) hasAccess(server, project, ref, email string, accessType remote.AccessType) (bool, error) {
	if s.evaluator == nil {
		return true, nil
	}

	if s.isMatchingUserRef(server, project, ref, email) {
		return true, nil
	}

	ent, err := remote.NewEntity(remote.UserPrefix + email)
	if err != nil {
		return false, err
	}
	query := remote.ACLQuery{
		Server:     server,
		Project:    project,
		RefPath:    ref,
		Entity:     ent,
		AccessType: accessType,
	}

	return s.evaluator.Evaluate(query)
}

func (s *ACLServer) requireUser(ctx context.Context) (remote.User, error) {
	user, ok := remote.UserFromContext(ctx)
	if !ok {
		return remote.User{}, errors.New("cannot determine user")
	}

	return user, nil
}

func (s *ACLServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	user, err := s.requireUser(ctx)
	if err != nil {
		return nil, err
	}

	filteredRefs := make([]string, 0, len(refs))
	for _, ref := range refs {
		hasAccess, err := s.hasAccess(server, project, ref, user.Email, remote.AuthRead)
		if err != nil {
			return nil, err
		}
		if !hasAccess {
			log.Printf("Denied read access to (%v, %v, %v) for %v", server, project, ref, user.Email)
			// Insert empty ref to play nice with API contract.
			filteredRefs = append(filteredRefs, "")
		} else {
			filteredRefs = append(filteredRefs, ref)
		}
	}

	return s.RemoteCloser.GetRefs(ctx, server, project, filteredRefs)
}

func (s *ACLServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	result, err := s.RemoteCloser.SearchRefs(ctx, server, project, refPrefix, limit)
	if err != nil {
		return nil, err
	}

	user, err := s.requireUser(ctx)
	if err != nil {
		return nil, err
	}

	filteredRefs := make([]entities.ProjectRefHash, 0, len(result))
	for _, rh := range result {
		hasAccess, err := s.hasAccess(server, rh.Project, rh.Ref, user.Email, remote.AuthRead)
		if err != nil {
			return nil, err
		}
		if !hasAccess {
			log.Printf("Denied read access to (%v, %v, %v) for %v", server, rh.Project, rh.Ref, user.Email)
			continue
		}

		filteredRefs = append(filteredRefs, rh)
	}

	return filteredRefs, nil
}

func (s *ACLServer) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	for _, rh := range refs {
		hasAccess, err := s.hasAccess(server, project, rh.Ref, user.Email, remote.AuthWrite)
		if err != nil {
			return err
		}
		if !hasAccess {
			return &remote.ErrPermissionDenied{Action: fmt.Sprintf("%q", rh.Ref)}
		}
	}

	if err := s.RemoteCloser.PutRefs(ctx, server, project, refs); err != nil {
		return err
	}

	// Invalidate the ACL evaluation caches if any ACL reference is updated.
	if s.evaluator != nil {
		for _, rh := range refs {
			if rh.Ref == remote.ACLRef {
				s.evaluator.DropCaches()
				break
			}
		}
	}

	return nil
}

func (s *ACLServer) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	if s.evaluator == nil {
		return s.RemoteCloser.AddMapResult(ctx, from, to)
	}

	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	ent, err := remote.NewEntity(remote.UserPrefix + user.Email)
	if err != nil {
		return err
	}

	hasAccess, err := s.evaluator.EvaluateMapACLs(ent, remote.AuthWrite)
	if err != nil {
		return err
	}
	if !hasAccess {
		return &remote.ErrPermissionDenied{Action: "writing map result"}
	}

	return s.RemoteCloser.AddMapResult(ctx, from, to)
}
