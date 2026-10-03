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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

func LinkValidationToStatus(e *remote.ErrObjectLinkValidation) (*status.Status, error) {
	st := status.New(codes.FailedPrecondition, "One or more of the provided object's links is missing. Details: "+e.Error())
	var err error
	for _, l := range e.MissingLinks {
		l := &pb.ObjectLink{Key: HashToBytes(l)}
		st, err = st.WithDetails(l)
		if err != nil {
			return nil, err
		}
	}
	return st, nil
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
