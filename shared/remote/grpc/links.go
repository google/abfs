// Copyright 2026 Google LLC
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

package grpc

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

// WrapErrorInStatus looks for specific typed errors (like ErrObjectLinkValidation or ErrPermissionDenied) and wraps them in a grpc Status error if found.
func WrapErrorInStatus(err error) error {
	var linkErr *remote.ErrObjectLinkValidation
	if errors.As(err, &linkErr) {
		st, err := LinkValidationToStatus(linkErr)
		if err != nil {
			return err
		}
		return st.Err()
	}

	var permErr *remote.ErrPermissionDenied
	if errors.As(err, &permErr) {
		return status.Error(codes.PermissionDenied, permErr.Error())
	}

	return err
}

// maxMissingLinksInStatus bounds the links listed in a missing-links status. The status is sent in
// the grpc-status-details-bin trailer, at about 84 bytes per listed link, and proxies limit the size
// of response headers (60 KB on some load balancers). 160 links make about 14 KB, which leaves room
// for other headers and longer hashes. Clients push the listed links and retry, so a partial list
// converges.
const maxMissingLinksInStatus = 160

func LinkValidationToStatus(e *remote.ErrObjectLinkValidation) (*status.Status, error) {
	seen := make(map[Hash]struct{}, len(e.MissingLinks))
	details := make([]protoadapt.MessageV1, 0, min(len(e.MissingLinks), maxMissingLinksInStatus))
	for _, l := range e.MissingLinks {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		if len(details) < maxMissingLinksInStatus {
			details = append(details, &pb.ObjectLink{Key: HashToBytes(l)})
		}
	}
	msg := fmt.Sprintf("One or more of the provided object's links is missing. Details: %d missing, %d listed", len(seen), len(details))
	return status.New(codes.FailedPrecondition, msg).WithDetails(details...)
}

// UnwrapMissingLinkError looks for a grpc status error that has any ObjectLink details and unwraps it
// into a native Go error if found.
func UnwrapMissingLinkError(err error) error {
	if s, ok := status.FromError(err); ok {
		var objs []Hash
		for _, detail := range s.Details() {
			switch t := detail.(type) {
			case *pb.ObjectLink:
				h := HashFromInputBytes(t.Key)
				objs = append(objs, h)
			}
		}
		if len(objs) > 0 {
			return &remote.ErrObjectLinkValidation{MissingLinks: objs}
		}
	}
	return err
}
